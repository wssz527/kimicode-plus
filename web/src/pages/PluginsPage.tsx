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
        title="插件"
        description="本地受信插件运行在当前界面里，按清单申请的能力取用核心功能。核心关闭全部插件后仍完整可用。"
      />
      <PluginSettings embedded />
    </div>
  );
}
