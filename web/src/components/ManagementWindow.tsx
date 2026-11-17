import { useEffect, useRef } from "react";
import { useAuth } from "../auth";
import { Spinner } from "./ui";
import { Icon } from "./Icon";
import type { ManagementPath, NativeHost } from "../native";
import { ModelsPage } from "../pages/ModelsPage";
import { CredentialsPage } from "../pages/CredentialsPage";
import "../styles/native-settings-surface.css";

const PAGES: { path: ManagementPath; label: string; hint: string }[] = [
  { path: "/models", label: "模型与供应商", hint: "目录来源、逐字段覆盖与回滚。" },
  { path: "/credentials", label: "凭据库", hint: "密钥只保存在增强程序自己的加密库里。" },
];

/**
 * The management window is its own BrowserWindow at a fixed route, so it does
 * not reuse the embedded navigation singleton the settings sheet depends on.
 * Moving to another management page reloads this window through the bridge,
 * which keeps one window and one API surface for the whole feature.
 */
export function ManagementWindow({ host, path }: { host: NativeHost; path: ManagementPath }) {
  const { phase } = useAuth();
  const mainRef = useRef<HTMLElement>(null);
  const page = PAGES.find(item => item.path === path) ?? PAGES[0]!;
  useEffect(() => {
    document.title = `${page.label} · KimiCode+ 管理`;
  }, [page.label]);
  return <div className="ekdm secondary-shell">
    <a className="skip-link" href="#main" onClick={event => { event.preventDefault(); mainRef.current?.focus(); }}>跳到主要内容</a>
    <header className="ekdm-header">
      <nav className="ekdm-nav" aria-label="管理页">
        {PAGES.map(item => <button
          key={item.path}
          type="button"
          className="ekdm-tab"
          aria-current={item.path === page.path ? "page" : undefined}
          onClick={() => { if (item.path !== page.path) void host.openManagement(item.path); }}
        >
          {item.label}
        </button>)}
      </nav>
      <button type="button" className="ekdm-close" title="关闭窗口" aria-label="关闭窗口" onClick={host.close}>
        <Icon name="close" size={15} />
      </button>
    </header>
    <main id="main" className="ekdm-main" tabIndex={-1} ref={mainRef}>
      {phase === "paired" ? <CurrentPage path={page.path} /> : null}
      {phase === "booting" || phase === "pairing" ? <p className="ekdm-status" role="status"><Spinner size={16} />正在连接增强核心</p> : null}
      {phase === "unpaired" ? <p className="ekdm-status">这个窗口没有连上增强核心。请从主窗口重新打开。</p> : null}
    </main>
  </div>;
}

function CurrentPage({ path }: { path: ManagementPath }) {
  if (path === "/credentials") return <CredentialsPage />;
  return <ModelsPage />;
}
