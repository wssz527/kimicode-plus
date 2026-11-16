import { useCallback, useEffect, useRef, useState } from "react";
import { followNativeSession, type NativeHost } from "../native";
import { ErrorNotice, Spinner } from "./ui";
import { RecoveryPanel, SessionInputProvider, SessionPane } from "./workbench/SessionPanes";
import { useNativeBootstrap } from "./Surfaces";

type SessionGridProps = { host: NativeHost; initialExtraIds: string[]; onNativeRestore: () => void };
export function SessionGrid(props: SessionGridProps) {
  return <SessionInputProvider><SessionGridBody {...props} /></SessionInputProvider>;
}
function SessionGridBody({ host, initialExtraIds, onNativeRestore }: SessionGridProps) {
  const { boot, error, reload } = useNativeBootstrap(host);
  const [ids, setIds] = useState(() => [host.activeSession(), ...initialExtraIds.slice(0, 2)].filter(Boolean));
  const [sessions, setSessions] = useState(boot?.sessions ?? []);
  const [recovery, setRecovery] = useState(boot?.recovery ?? null);
  const [weights, setWeights] = useState<number[]>([]);
  const [actionError, setActionError] = useState("");
  const [adding, setAdding] = useState(false);
  const addingRef = useRef(false);
  const idsRef = useRef(ids);
  idsRef.current = ids;
  const gridRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!boot) return;
    setSessions(boot.sessions);
    setRecovery(boot.recovery);
    setIds(current => current.length ? current : boot.sessions[0] ? [boot.sessions[0].id] : []);
  }, [boot]);
  useEffect(() => host.onActiveSession?.(id => {
    if (!id) return;
    setIds(current => followNativeSession(current, id));
    void reload();
  }), [host, reload]);
  useEffect(() => { setWeights(current => ids.map((_, index) => current[index] ?? 1)); }, [ids.length]);

  const add = useCallback(async () => {
    if (addingRef.current || idsRef.current.length >= 3) return;
    addingRef.current = true;
    setAdding(true);
    setActionError("");
    try {
      const id = await host.pickSession(idsRef.current);
      if (id) setIds(current => current.includes(id) || current.length >= 3 ? current : [...current, id]);
    } catch (cause) {
      if (!(cause instanceof Error && cause.message === "已取消选择")) setActionError(cause instanceof Error ? cause.message : String(cause));
    } finally { addingRef.current = false; setAdding(false); }
  }, [host]);
  useEffect(() => host.onAddSession?.(() => { void add(); }), [host, add]);

  function beginResize(event: React.PointerEvent<HTMLDivElement>, index: number) {
    event.preventDefault();
    const target = event.currentTarget;
    target.setPointerCapture(event.pointerId);
    const startX = event.clientX;
    const initial = ids.map((_, itemIndex) => weights[itemIndex] ?? 1);
    const width = gridRef.current?.getBoundingClientRect().width ?? 1;
    const move = (next: globalThis.PointerEvent) => {
      const delta = (next.clientX - startX) / width * ids.length;
      setWeights(current => {
        const values = ids.map((_, itemIndex) => current[itemIndex] ?? 1);
        const pair = initial[index]! + initial[index + 1]!;
        const first = Math.min(pair - 0.55, Math.max(0.55, initial[index]! + delta));
        values[index] = first; values[index + 1] = pair - first;
        return values;
      });
    };
    const finish = () => {
      target.removeEventListener("pointermove", move);
      target.removeEventListener("pointerup", finish);
      target.removeEventListener("pointercancel", finish);
    };
    target.addEventListener("pointermove", move);
    target.addEventListener("pointerup", finish);
    target.addEventListener("pointercancel", finish);
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
  const columns = ids.map((_, index) => weights[index] ?? 1);
  return <div className="session-grid wb">
    {actionError ? <p className="wb-error session-grid-error" role="alert">{actionError}</p> : null}
    <div className="session-grid-scroll"><div className="session-grid-panes" ref={gridRef} style={{ gridTemplateColumns: columns.map(weight => `minmax(300px, ${weight}fr)`).join(" ") }}>
      {ids.map((id, index) => <div className="session-grid-cell" key={index}>{index > 0 ? <div className="session-resizer" role="separator" aria-label={`调整会话 ${index} 与 ${index + 1} 的宽度`} aria-orientation="vertical" tabIndex={0} onPointerDown={event => beginResize(event, index - 1)} onKeyDown={event => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        const delta = event.key === "ArrowLeft" ? -0.1 : 0.1;
        setWeights(current => {
          const next = [...current]; const pair = (next[index - 1] ?? 1) + (next[index] ?? 1);
          const first = Math.min(pair - 0.55, Math.max(0.55, (next[index - 1] ?? 1) + delta));
          next[index - 1] = first; next[index] = pair - first; return next;
        });
      }} /> : null}<SessionPane host={host} index={index} sessions={sessions} models={boot.nativeModels} sessionId={id} onSessionId={next => changeSession(index, next)} onSessions={setSessions} onAdd={index === 0 ? () => void add() : undefined} canAdd={ids.length < 3 && !adding} onRestore={index === 0 ? onNativeRestore : undefined} onClose={index > 0 ? () => setIds(current => current.filter((_, itemIndex) => itemIndex !== index)) : undefined} /></div>)}
    </div></div>
    <RecoveryPanel host={host} state={recovery} onState={setRecovery} watchSession={ids[0] ?? ""} />
  </div>;
}
