import { PageHeader } from "../components/ui";
import { PluginSettings } from "../components/PluginSettings";

/**
 * The standalone workbench page. Plugin management itself lives in
 * PluginSettings so the settings dialog and this page render one implementation
 * instead of two that drift.
 */
export function PluginsPage() {
  return (
    <div className="page">
      <PageHeader
        title="增强插件"
        description="本地受信增强插件运行在当前界面里，按清单申请的能力取用核心功能，不改动官方插件市场。停用全部增强插件后核心仍完整可用。"
      />
      <PluginSettings embedded />
    </div>
  );
}
