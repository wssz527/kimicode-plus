import { useEffect, useState } from "react";
import type { DemoBootstrap, DemoHost, RecoveryState } from "../native";
import { Icon } from "./Icon";
import { ErrorNotice, Spinner } from "./ui";
import "../styles/workbench.css";
import { errorText } from "./workbench/pieces";
import { PlannerPane } from "./workbench/PlannerPane";
import { WebChatPane } from "./workbench/WebChatPane";
import { RecoveryPanel, SessionPane } from "./workbench/SessionPanes";

type Tab = "plan" | "web" | "exec";

const TABS: { id: Tab; label: string }[] = [
  { id: "plan", label: "规划聊天" },
  { id: "web", label: "网页聊天" },
  { id: "exec", label: "执行会话" },
];

export function Workbench({ host }: { host: DemoHost }) {
  const [tab, setTab] = useState<Tab>("plan");
  const [boot, setBoot] = useState<DemoBootstrap | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [sessions, setSessions] = useState<DemoBootstrap["sessions"]>([]);
  const [primary, setPrimary] = useState("");
  const [secondary, setSecondary] = useState("");
  const [split, setSplit] = useState(false);
  const [execWeb, setExecWeb] = useState(false);
  const [actionError, setActionError] = useState("");

  async function selectTab(next: Tab) {
    setActionError("");
    if (next === "web" && tab === "exec") {
      setExecWeb(true);
      try { await host.openBrowser("https://www.kimi.com/", "exec"); }
      catch (cause) { setActionError(errorText(cause)); }
      return;
    }
    setTab(next);
  }

  async function toggleSplit() {
    setActionError("");
    try {
      if (!split) await host.ensureWidth(860);
      setSplit(!split);
    } catch (cause) { setActionError(errorText(cause)); }
  }
  const [recovery, setRecovery] = useState<RecoveryState | null>(null);

  useEffect(() => {
    let alive = true;
    const active = host.activeSession();
    host
      .request<DemoBootstrap>("bootstrap", { ...(active ? { sourceSessionId: active } : {}) })
      .then((next) => {
        if (!alive) return;
        setBoot(next);
        setSessions(next.sessions);
        setRecovery(next.recovery);
        const first = active && next.sessions.some((s) => s.id === active) ? active : (next.sessions[0]?.id ?? "");
        setPrimary(first);
        setSecondary(next.sessions.find((s) => s.id !== first)?.id ?? "");
      })
      .catch((cause) => {
        if (alive) setError(errorText(cause));
      });
    return () => {
      alive = false;
    };
  }, [host]);

  if (error) {
    return (
      <div className="wb">
        <header className="wb-head">
          <span className="wb-mark" aria-hidden="true" />
          <h1>工作台</h1>
          <div className="wb-head-actions">
            <button type="button" className="wb-icon-btn" title="弹出独立窗口" onClick={() => void host.popout()}>
              <Icon name="external" size={15} />
            </button>
            <button type="button" className="wb-icon-btn" title="关闭" onClick={() => host.close()}>
              <Icon name="close" size={15} />
            </button>
          </div>
        </header>
        <div className="wb-body">
          <ErrorNotice title="工作台没有连上" message={error} recovery="请从 Kimi Code 的增强入口重新打开。" />
        </div>
      </div>
    );
  }

  if (!boot) {
    return (
      <div className="wb is-booting" role="status" aria-live="polite">
        <Spinner size={16} />
        <span>正在读取规划线程与会话</span>
      </div>
    );
  }

  return (
    <div className="wb">
      <header className="wb-head">
        <span className="wb-mark" aria-hidden="true" />
        <h1>工作台</h1>
        <nav className="wb-tabs" role="tablist" aria-label="工作台分区">
          {TABS.map((item) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={tab === item.id}
              className={`wb-tab ${tab === item.id ? "is-active" : ""}`}
              onClick={() => void selectTab(item.id)}
            >
              {item.label}
            </button>
          ))}
        </nav>
        <div className="wb-head-actions">
          <button type="button" className="wb-icon-btn" title="弹出独立窗口" onClick={() => void host.popout()}>
            <Icon name="external" size={15} />
          </button>
          <button type="button" className="wb-icon-btn" title="关闭" onClick={() => host.close()}>
            <Icon name="close" size={15} />
          </button>
        </div>
      </header>

      <div className="wb-body">
        {actionError ? <p className="wb-error" role="alert">{actionError}</p> : null}
        {tab !== "exec" ? <PlannerPane host={host} boot={{ ...boot, sessions }} hidden={tab !== "plan"} /> : null}
        {tab === "web" ? <WebChatPane host={host} boot={{ ...boot, sessions }} /> : null}
        {tab === "exec" ? (
          <div className="wb-exec-area">
            <div className="wb-bar">
              <span className="wb-label">执行会话</span>
              <button
                type="button"
                className={`wb-btn is-compact ${split ? "is-active" : ""}`}
                aria-pressed={split}
                onClick={() => void toggleSplit()}
              >
                <Icon name="overview" size={13} />
                并排两个
              </button>
            </div>
            {execWeb ? (
              <div className="wb-exec-webtools">
                <button className="wb-link" onClick={() => setExecWeb(false)}>收起网页工具</button>
                <WebChatPane host={host} boot={{ ...boot, sessions }} mode="exec" />
              </div>
            ) : null}
            <div className={`wb-panes ${split ? "is-split" : ""}`}>
              <SessionPane
                host={host}
                index={0}
                sessions={sessions}
                models={boot.nativeModels}
                sessionId={primary}
                onSessionId={setPrimary}
                onSessions={(next) => {
                  setSessions(next);
                  if (!secondary || secondary === primary) setSecondary("");
                }}
              />
              {split ? (
                <SessionPane
                  host={host}
                  index={1}
                  sessions={sessions}
                  models={boot.nativeModels}
                  sessionId={secondary}
                  onSessionId={setSecondary}
                  onSessions={setSessions}
                />
              ) : null}
            </div>

          </div>
        ) : null}
      </div>
      <RecoveryPanel host={host} state={recovery} onState={setRecovery} watchSession={primary} />
    </div>
  );
}
