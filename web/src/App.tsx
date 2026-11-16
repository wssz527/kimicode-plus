import { useEffect, type ReactNode } from "react";
import { AuthProvider, useAuth } from "./auth";
import { AppShell } from "./AppShell";
import { PairingScreen } from "./PairingScreen";
import { Spinner } from "./components/ui";
import { navigate, usePathname } from "./routes";
import { OverviewPage } from "./pages/OverviewPage";
import { ModelsPage } from "./pages/ModelsPage";
import { UsagePage } from "./pages/UsagePage";
import { ChatPage } from "./pages/ChatPage";
import { HandoffPage } from "./pages/HandoffPage";
import { PluginsPage } from "./pages/PluginsPage";
import { SettingsPage } from "./pages/SettingsPage";
import "./styles/base.css";
import "./styles/components.css";
import "./styles/app.css";
import "./styles/pages.css";
import "./styles/theme.css";
import { HomePage } from "./pages/HomePage";
import { WorkspacePage } from "./pages/WorkspacePage";
import { BrowserPage } from "./pages/BrowserPage";
import { CredentialsPage } from "./pages/CredentialsPage";
import { diagnosticMode } from "./host";
import { NativeSettings } from "./components/NativeSettings";

function Booting() {
  return (
    <div className="booting" role="status" aria-live="polite">
      <span className="booting-mark" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
      <div>
        <p className="booting-title">正在读取本地核心</p>
        <p className="booting-note">检查地址片段与内存会话，不会读取持久存储中的任何凭据。</p>
      </div>
      <Spinner size={18} />
    </div>
  );
}

function RoutedApp() {
  const pathname = usePathname();
  switch (pathname) {
    case "/models":
      return <ModelsPage />;
    case "/usage":
      return <UsagePage />;
    case "/chat":
      return <ChatPage />;
    case "/handoff":
      return <HandoffPage />;
    case "/plugins":
      return <PluginsPage />;
    case "/settings":
      return <SettingsPage />;
    case "/native-settings":
      return <NativeSettings />;
    case "/workspace":
      return <WorkspacePage />;
    case "/browser":
      return <BrowserPage />;
    case "/credentials":
      return <CredentialsPage />;
    case "/":
      return diagnosticMode() ? <OverviewPage /> : <HomePage />;
    default:
      return <NotFound pathname={pathname} />;
  }
}

function NotFound({ pathname }: { pathname: string }) {
  return (
    <div className="not-found">
      <p className="not-found-code num">404</p>
      <h1>没有这个工作台页面</h1>
      <p>
        地址 <code>{pathname}</code> 不存在，请返回增强设置继续。
      </p>
      <button className="btn btn-primary" type="button" onClick={() => navigate("/")}>
        返回增强设置
      </button>
    </div>
  );
}

function Gate({ children, embed }: { children: ReactNode; embed: boolean }) {
  const { phase } = useAuth();
  if (phase === "booting") return <Booting />;
  if (phase === "pairing") return <Booting />;
  if (phase !== "paired") return <PairingScreen />;
  return <AppShell embed={embed}>{children}</AppShell>;
}

export interface AppProps {
  embed?: boolean;
}

export function App({ embed = false }: AppProps = {}) {
  useEffect(() => {
    if (embed) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        const target = document.activeElement;
        if (target instanceof HTMLElement) target.blur();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [embed]);

  return (
    <AuthProvider>
      <Gate embed={embed}>
        <RoutedApp />
      </Gate>
    </AuthProvider>
  );
}
