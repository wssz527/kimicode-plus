import { useEffect, useRef, useState } from "react";
import type { DemoBootstrap, DemoHost } from "../../native";
import { Icon } from "../Icon";
import { Spinner } from "../ui";
import { Composer, HandoffForm, errorText, type HandoffMode } from "./pieces";
import { ControlMenu } from "./ControlMenu";

const SITES = [
  { label: "Kimi", url: "https://www.kimi.com/" },
  { label: "ChatGPT", url: "https://chatgpt.com/" },
  { label: "Gemini", url: "https://gemini.google.com/app" },
  { label: "DeepSeek", url: "https://chat.deepseek.com/" },
  { label: "Claude", url: "https://claude.ai/new" },
];

export function WebChatPane({ host, boot, mode = "plan", active = true, obscured = false }: { host: DemoHost; boot: DemoBootstrap; mode?: "plan" | "exec"; active?: boolean; obscured?: boolean }) {
  const [url, setUrl] = useState(SITES[0]!.url);
  const [opened, setOpened] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState("");
  const [handoffOpen, setHandoffOpen] = useState(false);
  const [handoffBusy, setHandoffBusy] = useState(false);
  const [handoffError, setHandoffError] = useState<string | null>(null);
  const [handoffResult, setHandoffResult] = useState<string | null>(null);
  const viewport = useRef<HTMLDivElement>(null);

  useEffect(() => host.subscribe<{ error?: string }>(`browser:${mode}`, value => {
    if (value.error) setError(value.error);
  }), [host, mode]);

  useEffect(() => {
    if (mode !== "plan" || !opened || handoffOpen || !active || obscured || menuOpen || !viewport.current) { void host.browserBounds({ visible: false }); return; }
    const element = viewport.current;
    let disposed = false;
    let tracking = false;
    let timer: number | undefined;
    let previous = "";
    const update = () => {
      const rect = element.getBoundingClientRect();
      const covered = Boolean(document.querySelector('.settings-dialog, .ekd-session-picker[open]'));
      const bounds = { visible: !covered && rect.width > 0 && rect.height > 0, x: rect.x, y: rect.y, width: rect.width, height: rect.height };
      const current = JSON.stringify(bounds);
      if (current === previous) return;
      previous = current;
      void host.browserBounds(bounds);
    };
    const track = () => {
      if (disposed) return;
      update();
      const moving = document.getAnimations().some(animation => {
        const target = animation.effect instanceof KeyframeEffect ? animation.effect.target : null;
        return animation.playState === "running" && target instanceof Element && (target === element || target.contains(element));
      });
      if (moving) timer = window.setTimeout(track, 16);
      else tracking = false;
    };
    const startTracking = () => {
      if (!tracking) { tracking = true; track(); }
    };
    const observer = new ResizeObserver(update);
    observer.observe(element);
    const overlays = new MutationObserver(update);
    overlays.observe(document.body, { childList: true, subtree: true });
    window.addEventListener("resize", update);
    document.addEventListener("transitionrun", startTracking, true);
    document.addEventListener("transitionend", update, true);
    document.addEventListener("transitioncancel", update, true);
    startTracking();
    return () => {
      disposed = true;
      observer.disconnect();
      overlays.disconnect();
      if (timer !== undefined) window.clearTimeout(timer);
      window.removeEventListener("resize", update);
      document.removeEventListener("transitionrun", startTracking, true);
      document.removeEventListener("transitionend", update, true);
      document.removeEventListener("transitioncancel", update, true);
      void host.browserBounds({ visible: false });
    };
  }, [host, mode, opened, handoffOpen, active, obscured, menuOpen]);

  async function openSite() {
    setBusy("open"); setError(null);
    try {
      await host.openBrowser(url, mode);
      setOpened(true); setHandoffOpen(false);
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(null); }
  }
  function startHandoff(text: string) {
    setPreview(text); setHandoffOpen(true); setHandoffResult(null); setHandoffError(null);
  }
  async function extract() {
    setBusy("extract"); setError(null);
    try {
      const text = await host.extractBrowser(mode);
      if (!text.trim()) { setError("没有取到网页正文，请选中文字后重试，或粘贴讨论内容。"); return; }
      startHandoff(text.trim());
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(null); }
  }
  async function submitHandoff(target: { sessionId?: string; mode: HandoffMode }) {
    setHandoffBusy(true); setHandoffError(null);
    try {
      const result = await host.request<{ sessionId: string }>("handoff", { targetId: target.sessionId, text: preview, mode: target.mode });
      setHandoffResult(result.sessionId);
    } catch (cause) { setHandoffError(errorText(cause)); }
    finally { setHandoffBusy(false); }
  }

  return (
    <div className={`wb-pane wb-web ${mode === "exec" ? "is-floating-tools" : ""}`}>
      <div className="wb-web-tools"><span>网页讨论</span><ControlMenu label="网页地址" icon="link" onOpenChange={setMenuOpen}>
      <div className="wb-web-row">
        <select className="wb-select" aria-label="网页站点" value={SITES.some(site => site.url === url) ? url : "custom"} onChange={event => setUrl(event.target.value === "custom" ? "" : event.target.value)}>
          {SITES.map(site => <option key={site.url} value={site.url}>{site.label}</option>)}
          <option value="custom">其他网页</option>
        </select>
        <input className="wb-input wb-web-address" aria-label="网页地址" value={url} onChange={event => setUrl(event.target.value)} />
        <button type="button" className="wb-btn" onClick={event => { event.currentTarget.closest<HTMLElement>('[popover]')?.hidePopover(); void openSite(); }} disabled={busy !== null || !url.trim()}>
          {busy === "open" ? <Spinner size={12} /> : <Icon name="external" size={13} />}{mode === "exec" ? "打开网页浮窗" : "打开网页"}
        </button>
        <button type="button" className="wb-btn" onClick={event => { event.currentTarget.closest<HTMLElement>('[popover]')?.hidePopover(); void extract(); }} disabled={busy !== null || !opened}>
          {busy === "extract" ? <Spinner size={12} /> : <Icon name="download" size={13} />}提取讨论
        </button>
      </div>
      <p className="wb-hint">{mode === "exec" ? "在独立窗口打开网页。" : "在当前面板打开网页。"} 选中讨论内容后提取，编辑再交接给 Code。</p>
      </ControlMenu><button type="button" className="wb-icon-btn" aria-label="提取网页讨论" title="提取网页讨论" disabled={busy !== null || !opened} onClick={() => void extract()}><Icon name="download" size={16} /></button></div>
      {error ? <p className="wb-error" role="alert">{error}</p> : null}
      {mode === "plan" && opened && !handoffOpen ? <div className="wb-browser-viewport" ref={viewport} aria-label="网页内容" /> : null}
      {handoffOpen ? (
        <div className="wb-web-preview">
          <button type="button" className="wb-link" onClick={() => setHandoffOpen(false)}>收起交接预览</button>
          <HandoffForm text={preview} onText={setPreview} sessions={boot.sessions} busy={handoffBusy} error={handoffError} result={handoffResult} onSubmit={target => void submitHandoff(target)} onOpenSession={id => void host.openSession(id)} />
        </div>
      ) : !opened && mode === "plan" ? (
        <div className="wb-web-fallback">
          <p className="wb-label">也可以直接粘贴网页讨论</p>
          <Composer value={draft} onChange={setDraft} onSend={() => { if (draft.trim()) { startHandoff(draft.trim()); setDraft(""); } }} placeholder="粘贴讨论内容，下一步选择目标会话" hint="确认交接后再交给 Code" />
        </div>
      ) : null}
    </div>
  );
}
