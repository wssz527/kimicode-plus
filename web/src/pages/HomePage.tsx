import { navigate } from "../routes";
import { useAuth } from "../auth";
import { Icon } from "../components/Icon";
import { ActionButton, PageHeader } from "../components/ui";

export function HomePage() {
  const { health } = useAuth();
  return <div className="page">
    <PageHeader
      title="增强设置"
      description="在这里管理本地核心与增强插件。接入 Kimi Code 后，可在客户端设置中使用原生增强。"
    />
    {health ? <p className="field-hint">核心版本 {health.version} · 安全模式{health.safeMode ? "已开启" : "已关闭"}</p> : null}
    <div className="feature-list">
      <button className="feature-row" type="button" onClick={() => navigate("/settings")}>
        <Icon name="settings" size={19} /><span><strong>核心设置</strong><small>管理安全模式、模块与 Windows 接入</small></span><span aria-hidden="true">›</span>
      </button>
      <button className="feature-row" type="button" onClick={() => navigate("/plugins")}>
        <Icon name="plugins" size={19} /><span><strong>增强插件</strong><small>查看、更新插件与管理授权</small></span><span aria-hidden="true">›</span>
      </button>
    </div>
    <ActionButton variant="quiet" onClick={() => navigate("/", { diagnostic: true })}>打开诊断工作台</ActionButton>
  </div>;
}
