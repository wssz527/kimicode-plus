import { useEffect, useRef, type ReactNode } from "react";
import { useAuth } from "./auth";
import { ActionButton, ErrorNotice } from "./components/ui";
import { ROUTES, navigate, usePathname, routeForPath } from "./routes";
import { errorRecovery } from "./lib/useResource";
import { diagnosticMode, hostConnection, useHostContext } from "./host";
import { embedRootClassName } from "./lib/embed";

export function AppShell({ children, embed = false }: { children: ReactNode; embed?: boolean }) {
  const { health, healthError, disconnect } = useAuth();
  const pathname = usePathname();
  const current = routeForPath(pathname);
  const host = hostConnection();
  const context = useHostContext();
  const diagnostic = diagnosticMode();
  useEffect(() => { if (!host && !embed) document.title = `${current.label} · 增强设置`; }, [current.label, host, embed]);
  const mainRef = useRef<HTMLElement>(null);
  const nested = pathname !== "/";
  return <div className={`${embedRootClassName(embed)} secondary-shell`}>
    <a className="skip-link" href="#main" onClick={e => { e.preventDefault(); mainRef.current?.focus(); }}>跳到主要内容</a>
    {embed ? null : <header className="secondary-header">
      <ActionButton variant="quiet" onClick={() => { if (nested) navigate("/"); else host?.back(); }} disabled={!nested && !host}>{nested ? "返回增强设置" : "返回工作区"}</ActionButton>
      <span>{nested ? current.label : "增强设置"}</span>
      <span className="secondary-context">{context?.title || context?.projectId || (host ? "当前工作区" : "本地预览 · 宿主未连接")}</span>
      {!host ? <select className="select select-sm theme-select" aria-label="外观" defaultValue={document.documentElement.dataset.colorScheme || "system"} onChange={e => { document.documentElement.dataset.colorScheme = e.target.value; }}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select> : null}
    </header>}
    {embed && nested ? <nav className="embedded-nav" aria-label="增强设置导航"><ActionButton variant="quiet" onClick={() => navigate("/")}>返回增强设置</ActionButton><span aria-current="page">{current.label}</span></nav> : null}
    {diagnostic ? <nav className="diagnostic-nav" aria-label="诊断导航">{ROUTES.map(route => <a key={route.path} href={route.path} onClick={e => { e.preventDefault(); navigate(route.path); }}>{route.label}</a>)}<details><summary>技术诊断</summary><pre>{JSON.stringify(health, null, 2)}</pre><ActionButton onClick={disconnect}>断开核心</ActionButton></details></nav> : null}
    {healthError ? <ErrorNotice message={healthError} recovery="请确认增强核心正在运行。" /> : null}
    <main id="main" className="secondary-main" tabIndex={-1} ref={mainRef}>{children}</main>
  </div>;
}
export function PageError({ error }: { error: unknown }) {
  if (!error) return null;
  return <ErrorNotice message={error instanceof Error ? error.message : String(error)} recovery={errorRecovery(error)} />;
}
