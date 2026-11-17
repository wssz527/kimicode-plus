import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Icon } from "./Icon";
import { ErrorNotice, Spinner } from "./ui";
import type { DemoBootstrap, NativeHost } from "../native";
import { errorText } from "./workbench/pieces";
import { PlannerPane } from "./workbench/PlannerPane";
import { WebChatPane } from "./workbench/WebChatPane";
import { ControlMenu } from "./workbench/ControlMenu";
export { SettingsWindow } from "./SettingsSurface";

export function useNativeBootstrap(host: NativeHost) {
  const [boot, setBoot] = useState<DemoBootstrap | null>(null);
  const [error, setError] = useState("");
  const reload = useCallback(async () => {
    setError("");
    try {
      const active = host.activeSession();
      setBoot(await host.request<DemoBootstrap>("bootstrap", active ? { sourceSessionId: active } : {}));
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [host]);
  useEffect(() => { void reload(); }, [reload]);
  return { boot, error, reload };
}

function SurfaceHeader({ title, host, children }: { title: string; host: NativeHost; children?: ReactNode }) {
  return <header className="surface-header">
    <div className="surface-title"><h1>{title}</h1></div>
    <div className="surface-actions">{children}
      <button type="button" className="wb-icon-btn" title="KimiCode+ 设置" aria-label="KimiCode+ 设置" onClick={() => void host.openSettings()}><Icon name="settings" size={15} /></button>
      <button type="button" className="wb-icon-btn" title="关闭" aria-label="关闭" onClick={host.close}><Icon name="close" size={15} /></button>
    </div>
  </header>;
}

export function PlannerSurface({ host, initialSource = "web" }: { host: NativeHost; initialSource?: "local" | "web" }) {
  const { boot, error, reload } = useNativeBootstrap(host);
  const [source, setSource] = useState<"local" | "web">(initialSource);
  const [menuOpen, setMenuOpen] = useState(false);
  useEffect(() => {
    const change = (event: Event) => {
      const next = (event as CustomEvent<string>).detail;
      if (next === "local" || next === "web") setSource(next);
    };
    window.addEventListener("ekd:planner-source", change);
    return () => window.removeEventListener("ekd:planner-source", change);
  }, []);
  function selectSource(next: "local" | "web", element: HTMLElement) {
    setSource(next);
    element.closest<HTMLElement>("[popover]")?.hidePopover();
  }
  if (error) return <div className="surface"><SurfaceHeader title="规划聊天" host={host} /><div className="surface-center"><ErrorNotice title="规划面板没有连上" message={error} recovery="检查增强核心后重试。" /><button className="wb-btn" onClick={() => void reload()}>重新连接</button></div></div>;
  if (!boot) return <div className="surface surface-center" role="status"><Spinner size={16} />正在读取规划线程</div>;
  return <div className="surface planner-surface wb">
    <SurfaceHeader title={source === "web" ? "网页规划" : "本地规划"} host={host}>
      <ControlMenu label="规划功能" icon="overview" onOpenChange={setMenuOpen}>
        <div className="wb-menu-options" role="group" aria-label="规划来源">
          <button type="button" aria-pressed={source === "web"} onClick={event => selectSource("web", event.currentTarget)}><Icon name="external" size={15} />网页规划{source === "web" ? <Icon name="check" size={14} /> : null}</button>
          <button type="button" aria-pressed={source === "local"} onClick={event => selectSource("local", event.currentTarget)}><Icon name="chat" size={15} />本地规划{source === "local" ? <Icon name="check" size={14} /> : null}</button>
          <button type="button" onClick={() => void host.popout(source)}><Icon name="external" size={15} />弹出规划面板</button>
        </div>
      </ControlMenu>
    </SurfaceHeader>
    <div className="surface-fill" hidden={source !== "local"}><PlannerPane host={host} boot={boot} /></div>
    <div className="surface-fill" hidden={source !== "web"}><WebChatPane host={host} boot={boot} active={source === "web"} obscured={menuOpen} /></div>
  </div>;
}
