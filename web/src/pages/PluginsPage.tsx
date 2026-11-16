import { useEffect, useState } from "react";
import { api, type PluginRecord } from "../api";
import { useAuth } from "../auth";
import { Icon } from "../components/Icon";
import {
  ActionButton,
  Badge,
  EmptyState,
  ErrorNotice,
  InfoNote,
  PageHeader,
  Panel,
  SkeletonRows,
  StateBadge,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useAction, useResource, errorMessage, errorRecovery } from "../lib/useResource";
import { fmtDateTime, fmtNumber, stateLabel, stateTone, truncate } from "../lib/format";

import { PortableTools } from "../components/PortableTools";
import { diagnosticMode } from "../host";
const PLUGIN_NOTES: Record<string, string> = {
  kct: "整理任务指令并预览脱敏结果，采用 Go 实现，不运行原版 KCT。",
  monitor: "导入用户提供的 Monitor 兼容事件，不依赖额外进程。",
  "fault-demo": "测试故障插件：验证启用失败时的隔离与清理。",
};

export function PluginsPage() {
  const { health } = useAuth();
  const plugins = useResource("plugins/list", () => api.plugins.list());
  const [pendingId, setPendingId] = useState<string | null>(null);
  const action = useAction<PluginRecord>();
  const [flash, setFlash] = useState<string | null>(null);
  const [showFaultConfirm, setShowFaultConfirm] = useState<string | null>(null);
  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(null), 3200);
    return () => clearTimeout(timer);
  }, [flash]);

  const items = (plugins.data?.items ?? []).filter(p => diagnosticMode() || p.id !== "fault-demo");
  const enabled = items.filter((plugin) => plugin.state === "ENABLED").length;
  const faulted = items.filter((plugin) => plugin.state === "FAULTED" || plugin.state === "FAILED").length;
  const resources = items.reduce((sum, plugin) => sum + (typeof plugin.resources === "number" ? plugin.resources : 0), 0);
  const safeMode = Boolean(health?.safeMode);

  async function run(id: string, kind: "ENABLE" | "DISABLE" | "FAULT") {
    setPendingId(id);
    setFlash(null);
    const result = await action.run(() => api.plugins.action(id, kind));
    setPendingId(null);
    setShowFaultConfirm(null);
    if (result) setFlash(`插件 ${result.name ?? result.id} 现在为 ${stateLabel(result.state)}。`);
    plugins.reload();
  }

  return (
    <div className="page">
      <PageHeader
        title="插件"
        description="按需启用可信的可移植处理组件。官方进程监控与原版 KCT 尚未连接，不启动 Node 服务。"
        meta={
          <>
            <span className="num">{enabled}/{items.length} 已启用</span>
            <span>{faulted} 个故障</span>
            <span>资源计数 {fmtNumber(resources)}</span>
            {safeMode ? <Badge tone="warn">安全模式 · 启停被阻止</Badge> : null}
          </>
        }
        actions={
          <ActionButton icon="refresh" onClick={plugins.reload} busy={plugins.loading}>
            刷新插件列表
          </ActionButton>
        }
      />

      {flash ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {flash}
        </div>
      ) : null}

      {safeMode ? (
        <InfoNote tone="warn">
          <p>
            安全模式正在开启。插件启用与故障触发都会被核心拒绝；已有状态和资源计数仍然可读。若要测试启停，请先在设置中关闭安全模式。
          </p>
        </InfoNote>
      ) : null}

      {action.error ? (
        <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
      ) : null}

      <PortableTools instructions={items.some(p => p.id === "kct" && p.enabled)} monitor={items.some(p => p.id === "monitor" && p.enabled)} />
      <div className="plugins-grid">
        <Panel title="内置插件" aside={<span className="num">{items.length}</span>}>
          {plugins.loading ? (
            <SkeletonRows rows={4} cols={3} />
          ) : plugins.error ? (
            <PageError error={plugins.error} />
          ) : items.length === 0 ? (
            <EmptyState icon="plugins" title="没有可用的内置插件">
              核心尚未注册插件模块。其余页面仍可使用。
            </EmptyState>
          ) : (
            <ul className="plugin-list">
              {items.map((plugin) => {
                const busy = action.busy && pendingId === plugin.id;
                const isEnabled = plugin.state === "ENABLED";
                const isFault = plugin.state === "FAULTED" || plugin.state === "FAILED";
                return (
                  <li key={plugin.id} className={isFault ? "plugin faulted" : "plugin"}>
                    <div className="plugin-head">
                      <div className="plugin-title">
                        <span className="plugin-glyph" aria-hidden="true">
                          <Icon name={plugin.kind === "FAULT_INJECTOR" ? "alert" : "plugins"} size={17} />
                        </span>
                        <div>
                          <strong>{plugin.name || plugin.id}</strong>
                          <span className="num plugin-id">{plugin.id}</span>
                        </div>
                      </div>
                      <div className="plugin-state">
                        <StateBadge state={plugin.state} tone={stateTone(plugin.state)} label={stateLabel(plugin.state)} />
                        <span className="num plugin-resources">
                          {typeof plugin.resources === "number" ? `${plugin.resources} 资源` : "资源 UNKNOWN"}
                        </span>
                      </div>
                    </div>
                    <p className="plugin-note">
                      {PLUGIN_NOTES[plugin.id] ?? plugin.description ?? "可信内置处理组件，没有外部代码入口。"}
                    </p>
                    {plugin.error ? (
                      <ErrorNotice message={plugin.error} recovery={safeMode ? "关闭安全模式后，可重新启用插件。" : plugin.kind === "FAULT_INJECTOR" ? "这是故障测试插件，其余插件可继续使用。" : "确认插件配置后，可重新启用插件。"} />
                    ) : null}
                    <div className="plugin-actions">
                      {isEnabled ? (
                        <ActionButton
                          icon="power"
                          onClick={() => void run(plugin.id, "DISABLE")}
                          busy={busy}
                          disabled={safeMode}
                        >
                          停用
                        </ActionButton>
                      ) : (
                        <ActionButton
                          variant="primary"
                          icon="power"
                          onClick={() => void run(plugin.id, "ENABLE")}
                          busy={busy}
                          disabled={safeMode}
                        >
                          启用
                        </ActionButton>
                      )}
                      {showFaultConfirm === plugin.id ? (
                        <div className="inline-confirm" role="group" aria-label="确认触发故障">
                          <span>触发故障会模拟失败并清理资源，确认？</span>
                          <ActionButton
                            variant="danger"
                            icon="alert"
                            onClick={() => void run(plugin.id, "FAULT")}
                            busy={busy}
                            disabled={safeMode}
                          >
                            确认触发
                          </ActionButton>
                          <ActionButton variant="quiet" onClick={() => setShowFaultConfirm(null)}>
                            取消
                          </ActionButton>
                        </div>
                      ) : (
                        <ActionButton
                          variant="quiet"
                          icon="alert"
                          onClick={() => setShowFaultConfirm(plugin.id)}
                          disabled={safeMode || !isEnabled}
                          title={isEnabled ? "模拟故障，验证隔离" : "只有已启用的插件才能触发故障"}
                        >
                          模拟故障
                        </ActionButton>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </Panel>

        <div className="plugins-rail">
          <Panel title="宿主约束">
            <ul className="limits-list">
              <li>插件采用可信内置 Go 处理能力，不加载外部代码或二进制。</li>
              <li>启用失败或故障不会影响核心读取、用量历史与 Outbox。</li>
              <li>安全模式下启用被拒绝；读取与停用状态不受影响。</li>
              <li>资源计数是模拟值，不能作为真实内存或句柄依据。</li>
            </ul>
          </Panel>

          <Panel title="资源与状态">
            <dl className="rail-facts">
              <div>
                <dt>总资源</dt>
                <dd className="num">{fmtNumber(resources)}</dd>
              </div>
              <div>
                <dt>已启用</dt>
                <dd className="num">{enabled}</dd>
              </div>
              <div>
                <dt>故障</dt>
                <dd className="num">{faulted}</dd>
              </div>
              <div>
                <dt>安全模式</dt>
                <dd>{safeMode ? "开启" : "关闭"}</dd>
              </div>
            </dl>
          </Panel>

          <Panel title="最近变化">
            {items.length === 0 ? (
              <p className="panel-note">没有插件记录。</p>
            ) : (
              <ul className="plugin-timeline">
                {items.map((plugin) => (
                  <li key={plugin.id}>
                    <span className="num">{fmtDateTime((plugin as { updatedAt?: string }).updatedAt)}</span>
                    <span>{truncate(plugin.name || plugin.id, 24)}</span>
                    <span className="num">{stateLabel(plugin.state)}</span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>
      </div>
    </div>
  );
}
