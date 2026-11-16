import { useEffect, useMemo, useState } from "react";
import {
  api,
  type Gateway,
  type QuotaSnapshot,
  type UsageEvent,
  type UsageFilters,
} from "../api";
import { Icon } from "../components/Icon";
import {
  ActionButton,
  Badge,
  DataTable,
  EmptyState,
  Field,
  InfoNote,
  PageHeader,
  Panel,
  SkeletonRows,
  Unknown,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useAction, useResource } from "../lib/useResource";
import {
  eventTokens,
  groupEvents,
  headlineFromSummary,
  histogram,
  type GroupBy,
  type TotalDisplay,
} from "../lib/usage";
import {
  fmtCompact,
  fmtDateTime,
  fmtMoney,
  fmtNumber,
  fmtTime,
  truncate,
} from "../lib/format";

import { UsageImport } from "../components/UsageImport";
import { diagnosticMode } from "../host";
const PAGE_LIMIT = 50;

export function UsagePage() {
  const [groupBy, setGroupBy] = useState<GroupBy>("provider");
  const [filters, setFilters] = useState<UsageFilters>({});
  const [draft, setDraft] = useState<UsageFilters>({});
  const [page, setPage] = useState(0);
  const [detail, setDetail] = useState<UsageEvent | null>(null);
  const queryKey = useMemo(() => JSON.stringify(filters), [filters]);

  const events = useResource(`usage/${queryKey}/${page}`, () =>
    api.usage.list(filters, page * PAGE_LIMIT, PAGE_LIMIT),
  );
  const summaryResource = useResource(`usage/summary/${queryKey}`, () => api.usage.summary(filters));
  const quotas = useResource("usage/quotas", () => api.usage.quotas());
  const gateways = useResource("usage/gateways", () => api.gateways());
  const demo = useAction<{ added: number; duplicates: number; unresolved: number }>();
  const cleanup = useAction<{events:number;unlinked:number;quotas:number;preserved:number}>();

  const shown = events.data?.items ?? [];
  const total = events.data?.total ?? 0;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_LIMIT));
  const pageStart = total === 0 ? 0 : page * PAGE_LIMIT + 1;
  const pageEnd = Math.min((page + 1) * PAGE_LIMIT, total);
  const headline = useMemo(() => headlineFromSummary(summaryResource.data), [summaryResource.data]);
  const groups = useMemo(() => groupEvents(shown, groupBy), [shown, groupBy]);
  const buckets = useMemo(() => histogram(shown, 7), [shown]);
  const maxBucket = Math.max(1, ...buckets.map((bucket) => bucket.volume));
  const quotaRows = toQuotaRows(quotas.data?.items ?? [], gateways.data?.items ?? []);
  const ledgerRows = quotas.data?.ledgers ?? [];
  const hasFilters = Object.values(filters).some((value) => typeof value === "string" && value.trim());

  useEffect(() => {
    if (page > 0 && page >= pageCount) setPage(pageCount - 1);
  }, [page, pageCount]);

  async function loadDemo() {
    const result = await demo.run(() => api.usage.demo());
    if (!result) return;
    events.reload();
    summaryResource.reload();
    quotas.reload();
    gateways.reload();
  }

  function applyFilters() {
    setPage(0);
    setFilters({ ...draft });
  }

  const loading = events.loading;
  const error = events.error;

  return (
    <div className="page">
      <PageHeader
        title="用量"
        description="实际记录、估算价格与订阅/网关额度分开呈现。读数只来自核心 summary；没有任何已知计数时显示 UNKNOWN，不用 0 补齐。"
        meta={
          <>
            <span className="num">
              第 {pageStart}–{pageEnd} 条 / 共 {fmtNumber(total)} 条
            </span>
            <span>
              {headline.available
                ? `${fmtNumber(headline.knownRequests)} 条计数完整 · ${fmtNumber(headline.unknownRequests)} 条 UNKNOWN`
                : "核心汇总读取中"}
            </span>
            <span>每页 {PAGE_LIMIT} 条 · 筛选由核心执行</span>
          </>
        }
        actions={
          <>
            <ActionButton
              icon="refresh"
              onClick={() => {
                events.reload();
                summaryResource.reload();
                quotas.reload();
                gateways.reload();
              }}
            >
              刷新读数
            </ActionButton>
            {diagnosticMode() ? <ActionButton icon="database" onClick={loadDemo} busy={demo.busy}>
              写入核心合成演示
            </ActionButton> : null}
            {diagnosticMode() ? <ActionButton busy={cleanup.busy} onClick={async () => {
              const result = await cleanup.run(() => api.usage.clearDemo());
              if (result) { events.reload(); summaryResource.reload(); quotas.reload(); }
            }}>清理合成演示</ActionButton> : null}
          </>
        }
      />

      {error ? <PageError error={error} /> : null}
      {summaryResource.error ? (
        <PageError error={summaryResource.error} />
      ) : null}

      {diagnosticMode() ? <InfoNote>
        <p>
          “写入核心合成演示”调用核心 <code>POST /usage/demo</code>：它确定性地写入本机独立 SQLite 账本
          （含一次真实重试与 Mock 额度快照），重复调用只增加 duplicates。页面随后重新读取核心
          events、summary 与 quotas。session-demo / project-demo 与 demo- 前缀标识合成记录，来源字段模拟可信级别。
          清理只删除这组固定演示及其额度；已有其他来源观测的请求会保留。共享 fixture 价格元数据保留，不参与独立记账。
        </p>
      </InfoNote> : null}
      <UsageImport onImported={() => { events.reload(); summaryResource.reload(); }} />

      {demo.result ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          核心写入完成：added {demo.result.added} · duplicates {demo.result.duplicates} ·
          unresolved {demo.result.unresolved}
        </div>
      ) : null}
      {demo.error ? <PageError error={demo.error} /> : null}
      {cleanup.error ? <PageError error={cleanup.error} /> : null}
      {cleanup.result ? <p role="status">已清理 {cleanup.result.events} 条演示请求、{cleanup.result.unlinked} 条未归因记录与 {cleanup.result.quotas} 条额度；保留 {cleanup.result.preserved} 条含其他观测的请求。</p> : null}

      <div className="reading-strip reading-strip-usage">
        <div className="reading">
          <span className="reading-label">请求数</span>
          <span className="reading-value num">
            {summaryResource.loading ? "…" : fmtNumber(headline.requests)}
          </span>
          <span className="reading-note">核心 summary</span>
        </div>
        <Readout label="输入 Token" display={headline.input} note="全部事件完整时为精确值" />
        <Readout label="输出 Token" display={headline.output} note="部分已知时显示 ≥ 部分和" />
        <Readout label="缓存读取" display={headline.cacheRead} note="按协议保留原值，不跨协议相加" />
        <div className="reading">
          <span className="reading-label">估算价格</span>
          <span className="reading-value num">
            {headline.estimate === null ? (
              <Unknown label="UNKNOWN" hint="核心没有给出可估算金额" />
            ) : (
              fmtMoney(headline.estimate, headline.currency)
            )}
          </span>
          <span className="reading-note">
            {headline.estimate === null
              ? "无价格来源或未定价"
              : headline.estimateComplete
                ? "按事件价格快照估算，不是账单"
                : "估算不完整：有未定价或计数缺失的事件"}
          </span>
        </div>
        <div className="reading reading-warn">
          <span className="reading-label">未知计数</span>
          <span className="reading-value num">{fmtNumber(headline.unknownRequests)}</span>
          <span className="reading-note">这些事件不参与完整合计</span>
        </div>
      </div>

      <Panel title="筛选">
        <form
          className="filter-form"
          onSubmit={(event) => {
            event.preventDefault();
            applyFilters();
          }}
        >
          <Field label="项目" htmlFor="usage-project">
            <input
              id="usage-project"
              className="input"
              value={draft.projectId ?? ""}
              onChange={(event) => setDraft({ ...draft, projectId: event.target.value })}
              placeholder="synthetic-project-workbench"
            />
          </Field>
          <Field label="会话" htmlFor="usage-session">
            <input
              id="usage-session"
              className="input"
              value={draft.sessionId ?? ""}
              onChange={(event) => setDraft({ ...draft, sessionId: event.target.value })}
              placeholder="synthetic-session-*"
            />
          </Field>
          <Field label="模型" htmlFor="usage-model">
            <input
              id="usage-model"
              className="input"
              value={draft.model ?? ""}
              onChange={(event) => setDraft({ ...draft, model: event.target.value })}
              placeholder="deepseek-flash"
            />
          </Field>
          <Field label="开始时间" htmlFor="usage-from" hint="ISO 8601">
            <input
              id="usage-from"
              className="input num"
              type="datetime-local"
              value={toLocalInput(draft.from)}
              onChange={(event) => setDraft({ ...draft, from: fromLocalInput(event.target.value) })}
            />
          </Field>
          <Field label="结束时间" htmlFor="usage-to" hint="ISO 8601">
            <input
              id="usage-to"
              className="input num"
              type="datetime-local"
              value={toLocalInput(draft.to)}
              onChange={(event) => setDraft({ ...draft, to: fromLocalInput(event.target.value) })}
            />
          </Field>
          <div className="filter-actions">
            <ActionButton type="submit" variant="primary" icon="search" busy={events.loading}>
              应用筛选
            </ActionButton>
            <ActionButton
              variant="quiet"
              onClick={() => {
                setDraft({});
                setFilters({});
                setPage(0);
              }}
            >
              清空
            </ActionButton>
          </div>
        </form>
      </Panel>

      <div className="usage-grid">
        <Panel
          title="历史事件"
          aside={
            <div className="segmented" role="group" aria-label="分组方式">
              <button
                type="button"
                className={groupBy === "provider" ? "is-active" : ""}
                aria-pressed={groupBy === "provider"}
                onClick={() => setGroupBy("provider")}
              >
                按供应商
              </button>
              <button
                type="button"
                className={groupBy === "session" ? "is-active" : ""}
                aria-pressed={groupBy === "session"}
                onClick={() => setGroupBy("session")}
              >
                按会话
              </button>
            </div>
          }
        >
          {loading ? (
            <SkeletonRows rows={6} cols={5} />
          ) : shown.length === 0 ? (
            <EmptyState
              icon="usage"
              title={hasFilters ? "没有符合筛选条件的用量事件" : "核心用量账本还是空的"}
              action={
                hasFilters ? (
                  <ActionButton
                    icon="refresh"
                    onClick={() => {
                      setDraft({});
                      setFilters({});
                      setPage(0);
                    }}
                  >
                    清除筛选
                  </ActionButton>
                ) : diagnosticMode() ? (
                  <ActionButton icon="database" onClick={loadDemo} busy={demo.busy}>
                    写入核心合成演示
                  </ActionButton>
                ) : null
              }
            >
              {hasFilters
                ? "筛选由核心执行，清空后仍会显示核心记录的全部事件。"
                : "可以调用核心确定性 demo 写入合成事件；它们会进入本机独立 SQLite，并可从表里读取。"}
            </EmptyState>
          ) : (
            <>
              <div className="usage-groups">
                {groups.slice(0, 6).map((group) => (
                  <div className="usage-group" key={group.key}>
                    <div className="usage-group-head">
                      <span className="num usage-group-key">{group.key}</span>
                      <span className="usage-group-count num">{group.requests} 次</span>
                    </div>
                    <div className="usage-group-bar">
                      <span
                        className="usage-group-fill"
                        style={{
                          width: `${Math.max(
                            4,
                            (group.volume / Math.max(1, groups.reduce((sum, item) => sum + item.volume, 0))) * 100,
                          )}%`,
                        }}
                      />
                    </div>
                    <dl className="usage-group-facts">
                      <div>
                        <dt>输入</dt>
                        <dd className="num">
                          <DisplayValue display={group.input} />
                        </dd>
                      </div>
                      <div>
                        <dt>输出</dt>
                        <dd className="num">
                          <DisplayValue display={group.output} />
                        </dd>
                      </div>
                      <div>
                        <dt>未知</dt>
                        <dd className="num">{group.unknown}</dd>
                      </div>
                    </dl>
                  </div>
                ))}
              </div>

              <DataTable
                caption={`当前页事件 · 第 ${pageStart}–${pageEnd} 条 / 共 ${fmtNumber(total)} 条`}
                columns={
                  <>
                    <th scope="col">时间</th>
                    <th scope="col">供应商 / 模型</th>
                    <th scope="col">会话</th>
                    <th scope="col">角色</th>
                    <th scope="col">输入</th>
                    <th scope="col">输出</th>
                    <th scope="col">缓存读</th>
                    <th scope="col" />
                  </>
                }
              >
                {shown.map((event) => {
                  const counts = eventTokens(event);
                  return (
                    <tr key={`${event.requestId}-${event.attemptId}-${event.at}`}>
                      <td className="num-cell">
                        {fmtTime(event.at)}
                        <span className="cell-sub">{event.source} · {event.observationState ?? "UNKNOWN"}</span>
                        {event.conflictStatus ? <span className="cell-sub">存在观测冲突：{event.conflictStatus}</span> : null}
                        <span className="cell-sub">{fmtDateTime(event.at).slice(0, 10)}</span>
                      </td>
                      <td>
                        <span className="num cell-strong">{event.model || "UNKNOWN"}</span>
                        <span className="cell-sub num">{event.providerId || "UNKNOWN"}</span>
                      </td>
                      <td className="num-cell cell-muted">{truncate(event.sessionId || "UNKNOWN", 22)}</td>
                      <td>
                        <Badge tone={event.role === "UNKNOWN" || !event.role ? "warn" : "neutral"}>
                          {event.role || "UNKNOWN"}
                        </Badge>
                      </td>
                      <td className="num-cell">{counts.input === null ? <Unknown /> : fmtNumber(counts.input)}</td>
                      <td className="num-cell">{counts.output === null ? <Unknown /> : fmtNumber(counts.output)}</td>
                      <td className="num-cell">{counts.cacheRead === null ? <Unknown /> : fmtNumber(counts.cacheRead)}</td>
                      <td>
                        <ActionButton variant="quiet" icon="info" onClick={() => setDetail(event)} title="查看原始观测">
                          <span className="sr-only">查看事件</span>
                        </ActionButton>
                      </td>
                    </tr>
                  );
                })}
              </DataTable>
              <nav className="pagination" aria-label="用量分页">
                <ActionButton
                  icon="chevronDown"
                  variant="quiet"
                  onClick={() => setPage((value) => Math.max(0, value - 1))}
                  disabled={page === 0 || loading}
                >
                  上一页
                </ActionButton>
                <span className="num pagination-status">
                  第 {page + 1} / {pageCount} 页 · 每页 {PAGE_LIMIT}
                </span>
                <ActionButton
                  icon="chevronUp"
                  variant="quiet"
                  onClick={() => setPage((value) => Math.min(pageCount - 1, value + 1))}
                  disabled={page >= pageCount - 1 || loading}
                >
                  下一页
                </ActionButton>
              </nav>
              <p className="table-footnote">
                分组与最近七天图只统计当前页；顶部读数与筛选由核心汇总执行。分项费用未提供——费用只显示核心
                summary 的估算总额。翻页可查看更早事件。
              </p>
            </>
          )}
        </Panel>

        <div className="usage-rail">
          <Panel title="最近 7 天">
            <div className="histogram" role="img" aria-label="最近七天输入与输出 Token 柱状图">
              {buckets.map((bucket) => {
                const total = bucket.total;
                return (
                  <div className="histogram-col" key={bucket.label}>
                    <span className="histogram-value num">
                      {total.kind === "unknown"
                        ? bucket.events > 0
                          ? "UNKNOWN"
                          : "—"
                        : total.kind === "partial"
                          ? `≥${fmtCompact(total.value)}`
                          : fmtCompact(total.value)}
                    </span>
                    <div className="histogram-stack" style={{ height: `${Math.max(3, (bucket.volume / maxBucket) * 100)}%` }}>
                      <span
                        className="histogram-input"
                        style={{
                          flex:
                            total.kind !== "unknown" && bucket.volume
                              ? knownShare(bucket.input, bucket.volume)
                              : 0,
                        }}
                      />
                      <span className="histogram-output" />
                    </div>
                    <span className="histogram-label num">{bucket.label}</span>
                  </div>
                );
              })}
            </div>
            <div className="legend">
              <span>
                <i className="legend-swatch legend-input" /> 输入
              </span>
              <span>
                <i className="legend-swatch legend-output" /> 输出
              </span>
            </div>
            <p className="panel-note">
              柱高为已知 Token 之和；有事件但计数全未知的日期显示 UNKNOWN，部分已知标 ≥，空日期显示 —。
            </p>
          </Panel>

          <Panel title="订阅与网关" aside={<Badge tone="warn">只读</Badge>}>
            {quotas.loading || gateways.loading ? (
              <SkeletonRows rows={3} cols={3} />
            ) : quotaRows.length === 0 ? (
              <EmptyState compact icon="database" title="没有额度快照">
                订阅额度与 API 余额是不同主体，不会相加；缺失时保持 UNKNOWN。
              </EmptyState>
            ) : (
              <ul className="quota-rows">
                {quotaRows.map((row) => (
                  <li key={row.id}>
                    <div className="quota-row-head">
                      <span>{row.label}</span>
                      <Badge>{row.kind}</Badge>
                    </div>
                    <div className="quota-row-value num">{row.value}</div>
                    <div className="quota-row-meta num">
                      <span>{row.source}</span>
                      <span>{fmtDateTime(row.observedAt)}</span>
                    </div>
                    {row.note ? <p className="quota-row-note">{row.note}</p> : null}
                  </li>
                ))}
                {ledgerRows.map((ledger) => (
                  <li key={`ledger-${ledger.ledger}`} className="quota-ledger">
                    <div className="quota-row-head">
                      <span>{ledger.label}</span>
                      <Badge tone={ledger.known ? "success" : "warn"}>{ledger.known ? "已知" : "UNKNOWN"}</Badge>
                    </div>
                    <div className="quota-row-value num">
                      {typeof ledger.remaining === "number" ? fmtNumber(ledger.remaining) : "UNKNOWN"}
                    </div>
                    <p className="quota-row-note">{ledger.note}</p>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          <Panel title="未知与估算说明">
            <ul className="limits-list">
              <li>费用只显示核心 summary 的 estimate 总额；分组与单条事件不展示前端计算的金额。</li>
              <li>缺失输入或输出计数的事件单独计为未知，不按 0 计入完整合计。</li>
              <li>OpenAI 口径中缓存读取属于输入子集；这里保留原值，不做跨协议相加。</li>
              <li>网页会员与 API 额度不映射，订阅余额只作只读快照。</li>
            </ul>
          </Panel>
        </div>
      </div>

      {detail ? (
        <div className="drawer-backdrop" role="presentation" onClick={() => setDetail(null)}>
          <section
            className="drawer drawer-narrow"
            role="dialog"
            aria-modal="true"
            aria-labelledby="event-detail-title"
            onClick={(event) => event.stopPropagation()}
          >
            <header className="drawer-head">
              <div>
                <h2 id="event-detail-title">原始观测</h2>
                <p className="num drawer-sub">{detail.requestId}</p>
              </div>
              <button type="button" className="icon-btn" aria-label="关闭事件详情" onClick={() => setDetail(null)}>
                <Icon name="close" size={18} />
              </button>
            </header>
            <div className="drawer-body">
              <dl className="detail-facts">
                <div>
                  <dt>供应商</dt>
                  <dd className="num">{detail.providerId || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>模型</dt>
                  <dd className="num">{detail.model || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>尝试</dt>
                  <dd className="num">{detail.attemptId || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>会话</dt>
                  <dd className="num">{detail.sessionId || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>Agent</dt>
                  <dd className="num">{detail.agentId || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>角色</dt>
                  <dd>{detail.role || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>协议</dt>
                  <dd className="num">{detail.protocol || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>来源</dt>
                  <dd className="num">{detail.source || "UNKNOWN"}</dd>
                </div>
                <div>
                  <dt>发生时间</dt>
                  <dd className="num">{fmtDateTime(detail.at)}</dd>
                </div>
              </dl>
              <h3 className="detail-heading">原始字段（协议归一只由核心完成）</h3>
              <pre className="code-block">{JSON.stringify(detail.raw, null, 2)}</pre>
              {detail.price ? (
                <>
                  <h3 className="detail-heading">价格快照（原始记录，不用于前端计算）</h3>
                  <pre className="code-block">{JSON.stringify(detail.price, null, 2)}</pre>
                </>
              ) : (
                <p className="panel-note">该事件没有价格快照；费用只在核心 summary 的估算总额中体现。</p>
              )}
            </div>
          </section>
        </div>
      ) : null}
    </div>
  );
}

function knownShare(input: TotalDisplay, volume: number): number {
  const inputValue = input.kind === "unknown" ? 0 : input.value;
  return Math.min(1, inputValue / Math.max(1, volume));
}

function DisplayValue({ display }: { display: TotalDisplay }) {
  if (display.kind === "unknown") return <Unknown label="UNKNOWN" hint="没有任何已知计数，不能显示为 0" />;
  if (display.kind === "partial") return <>≥ {fmtCompact(display.value)}</>;
  return <>{fmtCompact(display.value)}</>;
}

function Readout({ label, display, note }: { label: string; display: TotalDisplay; note: string }) {
  return (
    <div className="reading">
      <span className="reading-label">{label}</span>
      <span className="reading-value num">
        {display.kind === "unknown" ? (
          <Unknown label="UNKNOWN" hint="没有任何已知计数，不能显示为 0" />
        ) : display.kind === "complete" ? (
          fmtCompact(display.value)
        ) : (
          `≥ ${fmtCompact(display.value)}`
        )}
      </span>
      <span className="reading-note">
        {display.kind === "unknown"
          ? "无已知观测值"
          : display.kind === "complete"
            ? note
            : "部分已知 · 不完整"}
      </span>
    </div>
  );
}

interface QuotaRow {
  id: string;
  label: string;
  kind: string;
  value: string;
  source: string;
  observedAt: string | null;
  note?: string;
}

function toQuotaRows(quotas: QuotaSnapshot[], gateways: Gateway[]): QuotaRow[] {
  return [
    ...quotas.map((quota) => ({
      id: `quota-${quota.id}`,
      label: quota.providerId || quota.scope || quota.ledger,
      kind: quota.ledger,
      value:
        quota.remaining !== null
          ? `${quota.remaining}${quota.unit ? ` ${quota.unit}` : ""}`
          : quota.limit !== null
            ? `上限 ${quota.limit}${quota.unit ? ` ${quota.unit}` : ""} · 余额 UNKNOWN`
            : "UNKNOWN",
      source: quota.source,
      observedAt: quota.observedAt,
      note: quota.note,
    })),
    ...gateways.map((gateway) => ({
      id: `gateway-${gateway.id}`,
      label: gateway.name,
      kind: "网关",
      value:
        typeof gateway.balance === "number"
          ? `${fmtNumber(gateway.balance)} ${gateway.currency ?? ""}`.trim()
          : "UNKNOWN",
      source: gateway.note,
      observedAt: gateway.checkedAt,
    })),
  ];
}

function toLocalInput(value: string | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function fromLocalInput(value: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}
