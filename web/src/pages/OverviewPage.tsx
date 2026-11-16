import { useMemo, useState } from "react";
import { api } from "../api";
import { useAuth } from "../auth";
import { Icon } from "../components/Icon";
import {
  ActionButton,
  Badge,
  EmptyState,
  PageHeader,
  Panel,
  SkeletonRows,
  StateBadge,
  Unknown,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useResource } from "../lib/useResource";
import {
  directionLabel,
  fmtDateTime,
  fmtDuration,
  fmtNumber,
  stateLabel,
  stateTone,
  truncate,
} from "../lib/format";
import { navigate } from "../routes";
import type { Gateway, QuotaSnapshot } from "../api";

type QuotaRow = { id: string; label: string; value: string; source: string; observedAt: string | null };

function toQuotaRows(quotas: QuotaSnapshot[], gateways: Gateway[]): QuotaRow[] {
  return [
    ...quotas.map((quota) => ({
      id: `quota-${quota.id}`,
      label: quota.providerId || quota.scope || quota.ledger,
      value: quota.remaining ?? "UNKNOWN",
      source: quota.source,
      observedAt: quota.observedAt,
    })),
    ...gateways.map((gateway) => ({
      id: `gateway-${gateway.id}`,
      label: gateway.name,
      value: typeof gateway.balance === "number" ? `${gateway.balance} ${gateway.currency ?? ""}`.trim() : "UNKNOWN",
      source: gateway.note,
      observedAt: gateway.checkedAt,
    })),
  ];
}

export function OverviewPage() {
  const { health, healthError, refreshHealth } = useAuth();
  const tasks = useResource("overview/tasks", () => api.tasks.list(0, 12));
  const usage = useResource("overview/usage", () => api.usage.summary());
  const plugins = useResource("overview/plugins", () => api.plugins.list());
  const quotas = useResource("overview/quotas", () => api.usage.quotas());
  const gateways = useResource("overview/gateways", () => api.gateways());
  const [expanded, setExpanded] = useState<string | null>(null);

  const taskItems = tasks.data?.items ?? [];
  const delivered = taskItems.filter((task) => task.state === "DELIVERED").length;
  const unknown = taskItems.filter((task) => task.state === "UNKNOWN").length;
  const enabledPlugins = (plugins.data?.items ?? []).filter((plugin) => plugin.state === "ENABLED").length;
  const enabledModules = health?.modules.filter((module) => module.state === "READY").length ?? null;
  const summary = usage.data;

  const quotaRows = useMemo(
    () => toQuotaRows(quotas.data?.items ?? [], gateways.data?.items ?? []),
    [quotas.data, gateways.data],
  );

  return (
    <div className="page overview">
      <PageHeader
        title="交接工作台"
        description="从核心状态开始，选择来源消息，生成可预览的交接任务，再查看回执与历史。所有数值都来自本地增强服务；未知值保持 UNKNOWN。"
        meta={
          <>
            <span>核心 {health ? `v${health.version}` : "读取中"}</span>
            <span>运行 {health?.uptimeMs !== undefined ? fmtDuration(health.uptimeMs) : "UNKNOWN"}</span>
            <span>
              安全模式 {health ? (health.safeMode ? "开启" : "关闭") : "UNKNOWN"}
            </span>
          </>
        }
        actions={
          <>
            <ActionButton variant="primary" icon="handoff" onClick={() => navigate("/handoff")}>
              创建交接
            </ActionButton>
            <ActionButton
              icon="refresh"
              onClick={() => {
                refreshHealth();
                tasks.reload();
                usage.reload();
                plugins.reload();
              }}
            >
              刷新读数
            </ActionButton>
          </>
        }
      />

      {healthError ? <PageError error={new Error(healthError)} /> : null}

      <section className="overview-ledger" aria-label="核心读数">
        <div className="reading-strip">
          <div className="reading">
            <span className="reading-label">模块就绪</span>
            <span className="reading-value num">
              {enabledModules === null ? "UNKNOWN" : `${enabledModules}/${health?.modules.length ?? 0}`}
            </span>
            <span className="reading-note">核心注册的模块</span>
          </div>
          <div className="reading">
            <span className="reading-label">Outbox 记录</span>
            <span className="reading-value num">{tasks.loading ? "…" : fmtNumber(tasks.data?.total ?? null)}</span>
            <span className="reading-note">
              {tasks.data ? `${delivered} 已送达 · ${unknown} 未知` : "读取中"}
            </span>
          </div>
          <div className="reading">
            <span className="reading-label">用量请求</span>
            <span className="reading-value num">
              {usage.loading ? "…" : fmtNumber(summary?.requests ?? null)}
            </span>
            <span className="reading-note">
              {summary?.knownRequests !== undefined && summary?.knownRequests !== null
                ? `${fmtNumber(summary.knownRequests)} 条计数完整`
                : "计数完整性 UNKNOWN"}
            </span>
          </div>
          <div className="reading">
            <span className="reading-label">已启用插件</span>
            <span className="reading-value num">
              {plugins.loading ? "…" : `${enabledPlugins}/${plugins.data?.total ?? 0}`}
            </span>
            <span className="reading-note">仅模拟，不执行外部代码</span>
          </div>
        </div>

        <div className="overview-flow">
          <Panel
            title="最近交接"
            aside={
              <button type="button" className="link-btn link-btn-dark" onClick={() => navigate("/handoff")}>
                查看全部
              </button>
            }
          >
            {tasks.loading ? (
              <SkeletonRows rows={4} cols={4} />
            ) : tasks.error ? (
              <PageError error={tasks.error} />
            ) : taskItems.length === 0 ? (
              <EmptyState
                title="还没有交接记录"
                icon="handoff"
                action={
                  <ActionButton icon="plus" onClick={() => navigate("/handoff")}>
                    新建交接
                  </ActionButton>
                }
              >
                预览并确认后，任务会写入持久 Outbox。未确认的任务不会离开本地界面。
              </EmptyState>
            ) : (
              <ol className="ledger-list">
                {taskItems.slice(0, 6).map((task) => {
                  const open = expanded === task.id;
                  return (
                    <li key={task.id} className="ledger-row">
                      <div className="ledger-main">
                        <div className="ledger-line">
                          <StateBadge state={task.state} tone={stateTone(task.state)} label={stateLabel(task.state)} />
                          <span className="ledger-direction">{directionLabel(task.task.direction)}</span>
                          <span className="ledger-goal">
                            {truncate(task.task.goal || "无目标说明", 72)}
                          </span>
                        </div>
                        <div className="ledger-sub num">
                          <span>#{task.id.slice(0, 14)}</span>
                          <span>{fmtDateTime(task.createdAt ?? (task.receipt?.deliveredAt as string | undefined))}</span>
                          <span>checkpoint {task.task.checkpoint}</span>
                          <span>{task.task.sessionId}</span>
                        </div>
                      </div>
                      <button
                        type="button"
                        className="icon-btn"
                        aria-expanded={open}
                        aria-label={open ? "收起回执" : "展开回执"}
                        onClick={() => setExpanded(open ? null : task.id)}
                      >
                        <Icon name={open ? "chevronUp" : "chevronDown"} size={16} />
                      </button>
                      {open ? (
                        <div className="ledger-receipt">
                          {task.receipt ? (
                            <dl>
                              <div>
                                <dt>回执 ID</dt>
                                <dd className="num">{String(task.receipt.receiptId ?? "UNKNOWN")}</dd>
                              </div>
                              <div>
                                <dt>送达时间</dt>
                                <dd className="num">{fmtDateTime(task.receipt.deliveredAt)}</dd>
                              </div>
                              <div>
                                <dt>意图键</dt>
                                <dd className="num">{task.intent}</dd>
                              </div>
                            </dl>
                          ) : (
                            <p className="ledger-unknown">
                              没有回执。该记录需要核对，不能自动重发。请在任务交接页使用“核对回执”。
                            </p>
                          )}
                        </div>
                      ) : null}
                    </li>
                  );
                })}
              </ol>
            )}
          </Panel>

          <div className="flow-note">
            <Icon name="info" size={16} />
            <p>
              任务状态来自持久 Outbox。DELIVERED 表示 Mock 宿主确认；UNKNOWN 表示无法确认，只能核对，不会自动重试。
            </p>
          </div>
        </div>

        <aside className="overview-rail" aria-label="模块与适配器">
          <Panel title="核心与适配器">
            <div className="rail-block">
              <div className="rail-row">
                <span>服务状态</span>
                {health ? (
                  <StateBadge state={health.status} tone="success" label={`${health.status} · v${health.version}`} />
                ) : (
                  <Unknown />
                )}
              </div>
              <div className="rail-row">
                <span>Mock 适配器</span>
                <StateBadge
                  state={health?.adapter?.mode ?? "UNKNOWN"}
                  tone={stateTone(health?.adapter?.mode)}
                  label={stateLabel(health?.adapter?.mode)}
                />
              </div>
              <div className="rail-row">
                <span>压缩</span>
                <span className="rail-value">
                  {health?.compaction === "DISABLED_UNVERIFIED" ? "禁用 · 未验证" : (health?.compaction ?? "UNKNOWN")}
                </span>
              </div>
              <div className="rail-row">
                <span>凭据</span>
                <span className="rail-value">
                  {health?.credentials === "MEMORY_MOCK_ONLY" ? "仅内存 Mock" : (health?.credentials ?? "UNKNOWN")}
                </span>
              </div>
            </div>
          </Panel>

          <Panel title="模块" aside={<span className="num">{health?.modules.length ?? 0}</span>}>
            {health?.modules.length ? (
              <ul className="module-list">
                {health.modules.map((module) => (
                  <li key={module.id}>
                    <span className="module-name num">{module.id}</span>
                    <StateBadge state={module.state} tone={stateTone(module.state)} label={stateLabel(module.state)} />
                    {module.message ? <span className="module-message">{module.message}</span> : null}
                  </li>
                ))}
              </ul>
            ) : (
              <SkeletonRows rows={3} cols={2} />
            )}
          </Panel>

          <Panel title="额度与网关" aside={<Badge tone="warn">只读</Badge>}>
            {quotas.loading || gateways.loading ? (
              <SkeletonRows rows={3} cols={3} />
            ) : quotaRows.length === 0 ? (
              <EmptyState compact title="没有可读的余额快照" icon="database">
                订阅与网关余额分开记录；未提供时保持 UNKNOWN，不显示为 0。
              </EmptyState>
            ) : (
              <ul className="quota-list">
                {quotaRows.slice(0, 4).map((row) => (
                  <li key={row.id} title={row.source}>
                    <span>{row.label}</span>
                    <span className="num">{row.value}</span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </aside>
      </section>
    </div>
  );
}
