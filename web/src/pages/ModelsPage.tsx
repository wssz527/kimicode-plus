import { useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  type ModelChangeSnapshot,
  type ModelImportResult,
  type ModelPreview,
  type ModelRecord,
  type ModelsDevResult,
  type Provider,
  type ProviderPreset,
  type ProviderUpdateResult,
  type RollbackResult,
  type RollbackPreview,
} from "../api";
import { PublicCatalogImport } from "../components/PublicCatalogImport";
import { ProviderDiscovery } from "../components/ProviderDiscovery";
import { Icon } from "../components/Icon";
import {
  ActionButton,
  Badge,
  DataTable,
  EmptyState,
  ErrorNotice,
  Field,
  InfoNote,
  PageHeader,
  Panel,
  SkeletonRows,
  Unknown,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useAction, useResource, errorMessage, errorRecovery } from "../lib/useResource";
import { catalogRetrievedAt, isModelsDevCatalog, providerKeys, summarizeModelsDev } from "../lib/modelsdev";
import { renderPreview, type RenderedPreview } from "../lib/previewChanges";
import {
  capLabel,
  fmtDateTime,
  fmtNumber,
} from "../lib/format";
import { validateModelsDevFile } from "../lib/importFile";
import { MODEL_PAGE_LIMIT, buildModelPageQuery, buildProviderUpdateBody } from "../lib/contract";

import { validProviderId, validProviderURL, beginEdit, type EditDraft } from "../lib/providerPolicy";

import { diagnosticMode } from "../host";
const CAPABILITY_FIELDS = ["context", "output", "vision", "tools", "thinking", "audio"];

export function ModelsPage() {
  const providers = useResource("models/providers", () => api.providers.list(0, 200));
  const [page, setPage] = useState(0);
  const models = useResource(`models/models/${page}`, () => {
    const { offset, limit } = buildModelPageQuery(page, MODEL_PAGE_LIMIT);
    return api.models.list(offset, limit);
  });
  const presets = useResource("models/presets", () => api.providers.presets());
  const changes = useResource("models/changes", () => api.models.changes(0, 30));
  const [providerFilter, setProviderFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<EditDraft<ModelRecord> | null>(null);
  const [editError,setEditError] = useState<unknown>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const editRequest = useRef(0);
  useEffect(() => () => { editRequest.current++; }, []);

  async function openModel(model: ModelRecord) {
    const request = ++editRequest.current;
    setSelected(null);
    setEditError(null);
    try {
      const snapshot = await api.registry.snapshot(model.providerId, model.id);
      if (request !== editRequest.current) return;
      if (snapshot.model) setSelected(beginEdit(snapshot.model, snapshot.revision));
    } catch (error) {
      if (request === editRequest.current) setEditError(error);
    }
  }

  function closeModel() {
    editRequest.current++;
    setSelected(null);
  }

  const providerList = providers.data?.items ?? [];
  const modelList = models.data?.items ?? [];
  const totalModels = models.data?.total ?? 0;
  const pageCount = Math.max(1, Math.ceil(totalModels / MODEL_PAGE_LIMIT));
  const pageStart = totalModels === 0 ? 0 : page * MODEL_PAGE_LIMIT + 1;
  const pageEnd = Math.min((page + 1) * MODEL_PAGE_LIMIT, totalModels);
  const hasLocalFilter = providerFilter !== "all" || query.trim().length > 0;

  // 搜索与供应商筛选只作用于当前页（核心没有这些查询参数）；分页始终基于真实 total。
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return modelList.filter((model) => {
      if (providerFilter !== "all" && model.providerId !== providerFilter) return false;
      if (!needle) return true;
      return (
        model.id.toLowerCase().includes(needle) ||
        model.name.toLowerCase().includes(needle) ||
        model.providerId.toLowerCase().includes(needle)
      );
    });
  }, [modelList, providerFilter, query]);

  const unknownCount = filtered.filter((model) =>
    CAPABILITY_FIELDS.some((field) => !model.capabilities[field] || model.capabilities[field].source === "UNKNOWN"),
  ).length;

  useEffect(() => {
    if (page > 0 && page >= pageCount) setPage(pageCount - 1);
  }, [page, pageCount]);

  return (
    <div className="page">
      <PageHeader
        title="供应商与模型"
        description="每个能力字段独立保存值、来源与抓取时间。手动覆盖优先，未知保持 UNKNOWN；导入与回滚都带 revision 校验，预览不写入数据库。"
        meta={
          <>
            <span>
              {fmtNumber(totalModels)} 个模型 · {fmtNumber(providerList.length)} 个供应商
            </span>
            <span className="num">
              第 {pageStart}–{pageEnd} 条 / 共 {fmtNumber(totalModels)} 条
            </span>
            <span>{unknownCount} 个当前页模型存在 UNKNOWN 字段</span>
          </>
        }
        actions={
          <ActionButton
            icon="refresh"
            onClick={() => {
              providers.reload();
              models.reload();
              changes.reload();
            }}
          >
            重新读取目录
          </ActionButton>
        }
      />

      {editError ? <PageError error={editError} /> : null}
      <div className="models-layout">
        <div className="models-main">
          <Panel
            title="模型目录"
            aside={
              <div className="filter-row">
                <label className="sr-only" htmlFor="provider-filter">
                  按供应商筛选
                </label>
                <select
                  id="provider-filter"
                  className="select select-sm"
                  value={providerFilter}
                  onChange={(event) => setProviderFilter(event.target.value)}
                >
                  <option value="all">全部供应商</option>
                  {providerList.map((provider) => (
                    <option key={provider.id} value={provider.id}>
                      {provider.name}
                    </option>
                  ))}
                </select>
                <label className="sr-only" htmlFor="model-search">
                  搜索模型
                </label>
                <div className="input-with-icon">
                  <Icon name="search" size={15} />
                  <input
                    id="model-search"
                    className="input input-sm"
                    placeholder="搜索 ID 或名称"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                  />
                </div>
              </div>
            }
          >
            {models.loading ? (
              <SkeletonRows rows={6} cols={6} />
            ) : models.error ? (
              <PageError error={models.error} />
            ) : filtered.length === 0 ? (
              <EmptyState
                icon="models"
                title={modelList.length === 0 ? "目录尚未导入模型" : "没有匹配的模型"}
                action={
                  modelList.length === 0 ? (
                    <ActionButton icon="download" onClick={() => document.getElementById("import-section")?.scrollIntoView({ block: "start" })}>
                      导入 models.dev
                    </ActionButton>
                  ) : undefined
                }
              >
                {modelList.length === 0
                  ? "先添加供应商，再用预设发现或导入 models.dev JSON。缺失字段会保持 UNKNOWN，不会被自动填充。"
                  : "调整供应商筛选或搜索词。"}
              </EmptyState>
            ) : (
              <>
                <DataTable
                  caption={
                    hasLocalFilter
                      ? `模型目录 · 当前页 ${pageStart}–${pageEnd} / 共 ${totalModels}；筛选仅作用于本页，匹配 ${filtered.length} 条`
                      : `模型目录 · 第 ${pageStart}–${pageEnd} 条 / 共 ${fmtNumber(totalModels)} 条`
                  }
                  columns={
                    <>
                      <th scope="col">模型</th>
                      <th scope="col">上下文</th>
                      <th scope="col">输出</th>
                      <th scope="col">能力</th>
                      <th scope="col">来源</th>
                      <th scope="col">抓取时间</th>
                      <th scope="col">
                        <span className="sr-only">操作</span>
                      </th>
                    </>
                  }
                >
                  {filtered.map((model) => {
                    const context = model.capabilities.context;
                    const output = model.capabilities.output;
                    const flags = CAPABILITY_FIELDS.filter((field) => field !== "context" && field !== "output");
                    const sources = [...new Set(Object.values(model.capabilities).map((field) => field.source))];
                    const fetched = Object.values(model.capabilities)
                      .map((field) => field.fetchedAt)
                      .filter((value): value is string => typeof value === "string")
                      .sort()
                      .at(-1);
                    return (
                      <tr key={`${model.providerId}/${model.id}`}>
                        <td>
                          <strong className="cell-strong">{model.name || model.id}</strong>
                          <span className="cell-sub num">
                            {model.providerId}/{model.id}
                          </span>
                        </td>
                        <td className="num-cell">
                          {context?.value === null || context === undefined ? (
                            <Unknown />
                          ) : (
                            fmtNumber(Number(context.value))
                          )}
                        </td>
                        <td className="num-cell">
                          {output?.value === null || output === undefined ? (
                            <Unknown />
                          ) : (
                            fmtNumber(Number(output.value))
                          )}
                        </td>
                        <td>
                          <div className="cap-flags">
                            {flags.map((field) => (
                              <span
                                key={field}
                                className={`cap-flag ${flagClass(model.capabilities[field]?.value)}`}
                                title={`${capLabel(field)}：${capabilityText(model.capabilities[field]?.value)}`}
                              >
                                {capLabel(field)}
                              </span>
                            ))}
                          </div>
                        </td>
                        <td>
                          <div className="source-stack">
                            {sources.map((source) => (
                              <Badge
                                key={source}
                                tone={source === "manual" ? "accent" : source === "UNKNOWN" ? "warn" : "neutral"}
                              >
                                {source}
                              </Badge>
                            ))}
                          </div>
                        </td>
                        <td className="num-cell cell-muted">{fmtDateTime(fetched)}</td>
                        <td>
                          <ActionButton
                            variant="quiet"
                            icon="settings"
                            onClick={() => { void openModel(model); }}
                            title="打开字段覆盖"
                          >
                            <span className="sr-only">覆盖 {model.id}</span>
                          </ActionButton>
                        </td>
                      </tr>
                    );
                  })}
                </DataTable>
                <nav className="pagination" aria-label="模型分页">
                  <ActionButton
                    icon="chevronDown"
                    variant="quiet"
                    onClick={() => setPage((value) => Math.max(0, value - 1))}
                    disabled={page === 0 || models.loading}
                    title="上一页"
                  >
                    上一页
                  </ActionButton>
                  <span className="num pagination-status">
                    第 {page + 1} / {pageCount} 页 · 每页 {MODEL_PAGE_LIMIT}
                  </span>
                  <ActionButton
                    icon="chevronUp"
                    variant="quiet"
                    onClick={() => setPage((value) => Math.min(pageCount - 1, value + 1))}
                    disabled={page >= pageCount - 1 || models.loading}
                    title="下一页"
                  >
                    下一页
                  </ActionButton>
                </nav>
                {hasLocalFilter ? (
                  <p className="table-footnote">供应商与搜索框只在当前页内匹配；要查看更早的模型请翻页。</p>
                ) : null}
              </>
            )}
          </Panel>

          <Panel
            title="目录导入与变更"
            aside={<span className="num">预览不写入</span>}
            id="import-section"
          >
            <ImportWorkspace
              presetList={presets.data?.items ?? []}
              providerList={providerList}
              onChanged={() => {
                models.reload();
                changes.reload();
                providers.reload();
              }}
            />
          </Panel>

          <Panel title="变更快照" aside={<span className="num">{changes.data?.total ?? 0}</span>}>
            {changes.loading ? (
              <SkeletonRows rows={3} cols={4} />
            ) : changes.error ? (
              <PageError error={changes.error} />
            ) : (changes.data?.items.length ?? 0) === 0 ? (
              <EmptyState compact icon="clock" title="还没有变更快照">
                导入、覆盖或回滚成功后，核心会记录一个 revision 快照。
              </EmptyState>
            ) : (
              <ChangeSnapshots
                snapshots={changes.data?.items ?? []}
                onRollback={() => {
                  models.reload();
                  changes.reload();
                  providers.reload();
                }}
              />
            )}
          </Panel>
        </div>

        <aside className="models-rail">
          <ProviderPanel
            providers={providerList}
            presets={presets.data?.items ?? []}
            editing={editingProvider}
            onEdit={setEditingProvider}
            onChanged={() => {
              providers.reload();
              models.reload();
              changes.reload();
            }}
            loading={providers.loading}
            error={providers.error}
          />
          <Panel title="供应商目录"><ProviderDiscovery providers={providerList} onChanged={() => { models.reload(); changes.reload(); }} /></Panel>
          <Panel title="来源规则">
            <ul className="limits-list">
              <li>手动覆盖优先级最高，包括手动设为 UNKNOWN。</li>
              <li>目录导入只写提供到的字段，其余字段不会补猜测值。</li>
              <li>同名模型跨供应商不合并，不根据名称推断能力。</li>
              <li>预览展示 diff 与 revision；冲突时整批拒绝。</li>
            </ul>
          </Panel>
        </aside>
      </div>

      {selected ? (
        <OverrideDrawer
          key={`${selected.baseEntity.providerId}/${selected.baseEntity.id}/${selected.baseRevision}`}
          model={selected.baseEntity}
          baseRevision={selected.baseRevision}
          onClose={closeModel}
          onSaved={() => {
            models.reload();
            changes.reload();
          }}
        />
      ) : null}
    </div>
  );
}

function flagClass(value: unknown): string {
  if (value === true) return "cap-on";
  if (value === false) return "cap-off";
  if (value === null || value === undefined) return "cap-unknown";
  return "cap-on";
}

function capabilityText(value: unknown): string {
  if (value === true) return "支持";
  if (value === false) return "不支持";
  if (typeof value === "number") return String(value);
  return "UNKNOWN";
}

const PROTOCOLS = [
  { value: "openai", label: "openai · Chat Completions" },
  { value: "responses", label: "responses · OpenAI Responses" },
  { value: "anthropic", label: "anthropic · Messages" },
  { value: "gemini", label: "gemini · generateContent" },
];

function ProviderPanel({
  providers,
  presets,
  editing,
  onEdit,
  onChanged,
  loading,
  error,
}: {
  providers: Provider[];
  presets: ProviderPreset[];
  editing: string | null;
  onEdit: (id: string | null) => void;
  onChanged: () => void;
  loading: boolean;
  error: unknown;
}) {
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [protocol, setProtocol] = useState("openai");
  const [baseUrl, setBaseUrl] = useState("https://");
  const [secretRef, setSecretRef] = useState("");
  const [secretTouched, setSecretTouched] = useState(false);
  const [presetId, setPresetId] = useState("");
  const create = useAction<Provider>();
  const [creating, setCreating] = useState(false);
  const promptedEmpty = useRef(false);
  const [revision, setRevision] = useState<number | null>(null);
  const [conflict, setConflict] = useState<string | null>(null);
  const handledCreate = useRef<Provider | null>(null);

  const [providerBase,setProviderBase]=useState<EditDraft<Provider> | null>(null);
  const editingProvider = editing ? providerBase?.baseEntity ?? null : null;

  useEffect(() => {
    if (!create.result || handledCreate.current === create.result) return;
    handledCreate.current = create.result;
    setId("");
    setName("");
    setBaseUrl("https://");
    setSecretRef("");
    setSecretTouched(false);
    setPresetId("");
    setCreating(false);
    create.reset();
    onChanged();
  }, [create, onChanged]);

  // 只有确认核心返回空列表时才自动展开新建表单；加载中不再误展开。
  useEffect(() => {
    if (loading || promptedEmpty.current || providers.length > 0) return;
    promptedEmpty.current = true;
    setCreating(true);
  }, [loading, providers.length]);

  useEffect(() => {
    setProviderBase(null); setRevision(null);
    if (!editing) return;
    let cancelled=false;
    void api.registry.snapshot(editing).then(snapshot=> {
      if(cancelled || !snapshot.provider) return;
      const provider=snapshot.provider;
      setProviderBase(beginEdit(provider,snapshot.revision));
      setName(provider.name);setProtocol(provider.protocol);setBaseUrl(provider.baseUrl);
      setSecretRef(provider.secretRef ?? "");setSecretTouched(false);setConflict(null);setRevision(snapshot.revision);
    }).catch(error=> {if(!cancelled) setConflict(errorMessage(error));});
    return ()=>{cancelled=true};
  },[editing]);

  const update = useAction<ProviderUpdateResult>();

  useEffect(() => {
    if (update.result) {
      onEdit(null);
      update.reset();
      onChanged();
    }
  }, [update, onChanged, onEdit]);

  function applyPreset(nextId: string) {
    setPresetId(nextId);
    const preset = presets.find((item) => item.id === nextId);
    if (!preset) return;
    if (!id) setId(preset.id);
    if (!name) setName(preset.name);
    if (typeof preset.protocol === "string") setProtocol(preset.protocol);
    if (typeof preset.baseUrl === "string" && preset.baseUrl) setBaseUrl(preset.baseUrl);
  }

  function resetForm() {
    setId("");
    setName("");
    setProtocol("openai");
    setBaseUrl("https://");
    setSecretRef("");
    setSecretTouched(false);
    setPresetId("");
    setConflict(null);
  }

  const idValid = validProviderId(id);
  const urlValid = validProviderURL(baseUrl);
  const createReady = idValid && name.trim().length > 0 && urlValid && protocol.trim().length > 0;
  const updateReady =
    Boolean(editingProvider) &&
    name.trim().length > 0 &&
    urlValid &&
    protocol.trim().length > 0 &&
    revision !== null &&
    (secretTouched || (editingProvider?.secretRef ?? null) === null || secretRef === (editingProvider?.secretRef ?? ""));

  async function saveEditing() {
    if (!editingProvider || revision === null) return;
    const currentRef = editingProvider.secretRef ?? null;
    const nextRef = secretTouched ? (secretRef.trim() ? secretRef.trim() : null) : currentRef;
    const result = await update.run(() => {
      // 全量语义：未编辑时原样回传原 secretRef 引用；绝不读取或展示真实密钥。
      const body = buildProviderUpdateBody({
        name,
        protocol,
        baseUrl,
        secretRef: nextRef,
        revision,
      });
      return api.providers.update(editingProvider.id, body, revision);
    });
    if (!result && update.error) {
      const code = (update.error as { code?: string }).code;
      if (code === "STALE_REVISION" || code === "CONFIG_CONFLICT") {
        setConflict("供应商配置已被其他写入更新。请重新读取后再保存，避免覆盖并发修改。");
      }
    }
  }

  const busy = create.busy || update.busy;
  const activeError = update.error ?? create.error;
  const clearError = () => {
    update.clearError();
    create.clearError();
  };

  return (
    <Panel
      title="供应商"
      aside={editing ? undefined : <ButtonAdd showForm={creating} setShowForm={setCreating} />}
    >
      {loading ? (
        <SkeletonRows rows={3} cols={2} />
      ) : error ? (
        <PageError error={error} />
      ) : (
        <>
          <ul className="provider-list">
            {providers.map((provider) => {
              const isEditing = editing === provider.id;
              return (
                <li key={provider.id} className={isEditing ? "is-editing" : undefined}>
                  <div>
                    <strong>{provider.name}</strong>
                    <span className="num provider-id">{provider.id}</span>
                  </div>
                  <div className="provider-meta">
                    <Badge>{provider.protocol}</Badge>
                    <span className="num provider-url" title={provider.baseUrl}>
                      {provider.baseUrl}
                    </span>
                    {provider.secretRef ? <Badge tone="neutral">引用凭据</Badge> : null}
                    {provider.baseUrl === "urn:unconfigured" ? <Badge tone="warn">占位</Badge> : null}
                    {isEditing ? null : (
                      <ActionButton
                        variant="quiet"
                        icon="settings"
                        onClick={() => {
                          setCreating(false);
                          onEdit(provider.id);
                        }}
                        title="编辑配置"
                      >
                        编辑
                      </ActionButton>
                    )}
                  </div>
                </li>
              );
            })}
            {providers.length === 0 ? (
              <li className="provider-empty">还没有供应商。添加后即可导入或发现模型。</li>
            ) : null}
          </ul>

          {editingProvider ? (
            <form
              className="stack-form"
              aria-label={`编辑供应商 ${editingProvider.id}`}
              onSubmit={(event) => {
                event.preventDefault();
                if (!updateReady) return;
                void saveEditing();
              }}
            >
              <p className="import-copy">
                编辑 <code>{editingProvider.id}</code> 的名称、协议与地址。ID 不可修改；保存会携带 revision
                并与模型目录共用同一版本号，冲突时不会覆盖他人修改。
              </p>
              <div className="form-grid">
                <Field label="显示名称" htmlFor="provider-edit-name" required>
                  <input
                    id="provider-edit-name"
                    className="input"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                  />
                </Field>
                <Field label="协议" htmlFor="provider-edit-protocol" required>
                  <select
                    id="provider-edit-protocol"
                    className="select"
                    value={protocol}
                    onChange={(event) => setProtocol(event.target.value)}
                  >
                    {PROTOCOLS.map((item) => (
                      <option key={item.value} value={item.value}>
                        {item.label}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field
                  label="Base URL"
                  htmlFor="provider-edit-url"
                  required
                  hint="HTTPS 或 loopback HTTP，不含查询参数或凭据。导入产生的占位地址可在此改为真实端点。"
                >
                  <input
                    id="provider-edit-url"
                    className="input num"
                    value={baseUrl}
                    onChange={(event) => setBaseUrl(event.target.value)}
                    aria-invalid={!urlValid}
                  />
                </Field>
                <Field
                  label="secretRef（凭据引用）"
                  htmlFor="provider-edit-secret"
                  hint={
                    editingProvider.secretRef
                      ? "当前已保存引用。留空并保存会清除引用；本页从不读取真实密钥。"
                      : "只接受凭据库引用名，不接受内联密钥。"
                  }
                >
                  <input
                    id="provider-edit-secret"
                    className="input num"
                    value={secretRef}
                    onChange={(event) => {
                      setSecretTouched(true);
                      setSecretRef(event.target.value);
                    }}
                    placeholder="例如 keychain://providers/example"
                  />
                </Field>
              </div>
              <p className="num field-hint">当前 revision {revision ?? "读取中…"}</p>
              {conflict ? (
                <InfoNote tone="warn">
                  <p>{conflict}</p>
                </InfoNote>
              ) : null}
              {activeError ? (
                <ErrorNotice
                  message={errorMessage(activeError)}
                  recovery={errorRecovery(activeError)}
                  onDismiss={clearError}
                />
              ) : null}
              <div className="form-actions">
                <ActionButton
                  type="submit"
                  variant="primary"
                  icon="check"
                  disabled={!updateReady || busy}
                  busy={update.busy}
                >
                  保存供应商
                </ActionButton>
                <ActionButton
                  variant="quiet"
                  onClick={() => {
                    onEdit(null);
                    update.reset();
                    setConflict(null);
                  }}
                >
                  取消
                </ActionButton>
              </div>
            </form>
          ) : null}

          {creating && !editingProvider ? (
            <form
              className="stack-form"
              onSubmit={(event) => {
                event.preventDefault();
                if (!createReady) return;
                void create.run(() =>
                  api.providers.create({
                    id: id.trim(),
                    name: name.trim(),
                    protocol: protocol.trim(),
                    baseUrl: baseUrl.trim(),
                    ...(secretRef.trim() ? { secretRef: secretRef.trim() } : {}),
                  }),
                );
              }}
            >
              <Field label="使用预设" htmlFor="provider-preset" hint="预设只填写协议与地址，不包含任何真实密钥。">
                <select
                  id="provider-preset"
                  className="select"
                  value={presetId}
                  onChange={(event) => applyPreset(event.target.value)}
                >
                  <option value="">手动填写</option>
                  {presets.map((preset) => (
                    <option key={preset.id} value={preset.id}>
                      {preset.name}
                    </option>
                  ))}
                </select>
              </Field>
              <div className="form-grid">
                <Field label="供应商 ID" htmlFor="provider-id" required hint="以小写字母或数字开头，后接小写字母、数字、点、下划线或连字符，共 1–64 字符。">
                  <input
                    id="provider-id"
                    className="input num"
                    value={id}
                    onChange={(event) => setId(event.target.value)}
                    placeholder="deepseek"
                    aria-invalid={id.length > 0 && !idValid}
                  />
                </Field>
                <Field label="显示名称" htmlFor="provider-name" required>
                  <input
                    id="provider-name"
                    className="input"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    placeholder="DeepSeek"
                  />
                </Field>
                <Field label="协议" htmlFor="provider-protocol" required>
                  <select
                    id="provider-protocol"
                    className="select"
                    value={protocol}
                    onChange={(event) => setProtocol(event.target.value)}
                  >
                    {PROTOCOLS.map((item) => (
                      <option key={item.value} value={item.value}>
                        {item.label}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="Base URL" htmlFor="provider-url" required hint="HTTPS 或 loopback HTTP，不包含查询参数或凭据。">
                  <input
                    id="provider-url"
                    className="input num"
                    value={baseUrl}
                    onChange={(event) => setBaseUrl(event.target.value)}
                    placeholder="https://api.example.com/v1"
                    aria-invalid={baseUrl !== "https://" && !urlValid}
                  />
                </Field>
                <Field label="secretRef（可选）" htmlFor="provider-secret" hint="凭据库引用名；不接受内联密钥，也不会读取真实值。">
                  <input
                    id="provider-secret"
                    className="input num"
                    value={secretRef}
                    onChange={(event) => setSecretRef(event.target.value)}
                    placeholder="keychain://providers/example"
                  />
                </Field>
              </div>
              {activeError ? (
                <ErrorNotice message={errorMessage(activeError)} recovery={errorRecovery(activeError)} onDismiss={clearError} />
              ) : null}
              <div className="form-actions">
                <ActionButton type="submit" variant="primary" icon="plus" disabled={!createReady || busy} busy={create.busy}>
                  添加供应商
                </ActionButton>
                <ActionButton
                  variant="quiet"
                  onClick={() => {
                    setCreating(false);
                    resetForm();
                  }}
                >
                  取消
                </ActionButton>
              </div>
            </form>
          ) : null}
        </>
      )}
    </Panel>
  );
}

function ButtonAdd({ showForm, setShowForm }: { showForm: boolean; setShowForm: (value: boolean) => void }) {
  if (showForm) return null;
  return (
    <ActionButton variant="quiet" icon="plus" onClick={() => setShowForm(true)}>
      添加
    </ActionButton>
  );
}

function ImportWorkspace({
  presetList,
  providerList,
  onChanged,
}: {
  presetList: ProviderPreset[];
  providerList: Provider[];
  onChanged: () => void;
}) {
  const [providerId, setProviderId] = useState(providerList[0]?.id ?? "");
  const [discovered, setDiscovered] = useState<ModelRecord[]>([]);
  const [preview, setPreview] = useState<ModelRecord[] | null>(null);
  // 差异只来自 Go /models/preview 与 /models/modelsdev 的返回，客户端不做合并推算。
  const [goPreview, setGoPreview] = useState<RenderedPreview | null>(null);
  const [catalogText, setCatalogText] = useState("");
  const [catalog, setCatalog] = useState<unknown>(null);
  const [catalogFile, setCatalogFile] = useState<string | null>(null);
  const [catalogPreview, setCatalogPreview] = useState<number | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const discover = useAction<ModelRecord[]>();
  const previewAction = useAction<ModelPreview>();
  // 预览时记录的 revision 必须原样用于导入，不能回退到全局缓存值。
  const [discoveryRevision, setDiscoveryRevision] = useState<number | null>(null);
  const importAction = useAction<ModelImportResult>();
  const modelsDevAction = useAction<ModelsDevResult>();
  const [fileError, setFileError] = useState<string | null>(null);

  useEffect(() => {
    if (!providerId && providerList[0]) setProviderId(providerList[0].id);
  }, [providerId, providerList]);

  const summary = useMemo(() => (catalog ? summarizeModelsDev(catalog) : null), [catalog]);
  const keys = useMemo(() => (catalog ? providerKeys(catalog) : []), [catalog]);
  const retrievedAt = useMemo(() => (catalog ? catalogRetrievedAt(catalog) : null), [catalog]);

  async function runDiscovery() {
    if (!providerId) return;
    const records = await discover.run(() => api.models.discovery(providerId));
    if (records) {
      setDiscovered(records);
      setPreview(null);
      setDiscoveryRevision(null);
      setGoPreview(null);
      setNotice(`发现 ${records.length} 条本地 Mock 记录，尚未写入。`);
    }
  }

  async function runPreview() {
    if (!providerId) return;
    const records = discovered.filter((record) => record.providerId === providerId);
    if (records.length === 0) {
      setNotice("没有可预览的记录，请先执行本地发现。");
      return;
    }
    const result = await previewAction.run(() => api.models.preview(records));
    if (result) {
      setPreview(records);
      setDiscoveryRevision(result.revision);
      setGoPreview(renderPreview(result));
      setNotice(
        result.changes.length === 0
          ? `预览 revision ${result.revision}：无有效变更（同值或 manual 覆盖仍生效）。`
          : `预览 revision ${result.revision}，${result.changes.length} 个模型有变化；尚未写入。`,
      );
    }
  }

  async function runImport() {
    if (!providerId || !preview || discoveryRevision === null) return;
    const revision = discoveryRevision;
    const result = await importAction.run(() => api.models.import(preview, revision));
    if (result) {
      setNotice(`已导入 ${result.imported} 条，revision ${result.revision}，changeId ${result.changeId}。`);
      setPreview(null);
      setDiscoveryRevision(null);
      setDiscovered([]);
      setGoPreview(null);
      onChanged();
    }
  }

  async function loadCatalogFile(file: File) {
    setFileError(null);
    const result = await validateModelsDevFile(file);
    if (result.error) {
      setFileError(result.error);
      return;
    }
    setCatalogText(result.text);
    setCatalog(result.catalog);
    setCatalogFile(file.name);
    setCatalogPreview(null);
  }

  async function previewCatalog() {
    if (!catalog) return;
    if (!catalogRetrievedAt(catalog)) {
      setFileError("目录缺少有效的 retrieved_at（RFC3339）。请使用有依据的抓取时间；页面不会伪造为当前时间。");
      return;
    }
    const revision = await api.models.revision();
    const result = await modelsDevAction.run(() =>
      api.models.modelsdev(catalog, { preview: true, revision }),
    );
    if (result) {
      setCatalogPreview(result.revision);
      setGoPreview(
        renderPreview({
          revision: result.revision,
          changes: result.changes ?? [],
          summary: result.summary ?? { created: 0, updated: 0, unchanged: 0 },
        }),
      );
      const changes = result.changes ?? [];
      setNotice(
        changes.length === 0
          ? `models.dev 预览 revision ${result.revision}：无有效变更。`
          : `models.dev 预览 revision ${result.revision}，${changes.length} 个模型有变化；尚未写入。`,
      );
    }
  }

  async function importCatalog() {
    if (!catalog || catalogPreview === null) return;
    const revision = catalogPreview;
    const result = await modelsDevAction.run(() =>
      api.models.modelsdev(catalog, { preview: false, revision }),
    );
    if (result) {
      setNotice(`models.dev 目录已事务导入，revision ${result.revision}。`);
      setCatalogPreview(null);
      setGoPreview(null);
      onChanged();
    }
  }

  const actionError = discover.error ?? previewAction.error ?? importAction.error ?? modelsDevAction.error;

  return (
    <div className="import-workspace">
      <PublicCatalogImport providers={providerList} onChanged={onChanged} />
      <div className="import-columns">
        <section hidden={!diagnosticMode()} className="import-card" aria-labelledby="discovery-title">
          <h3 id="discovery-title">本地 Mock 发现</h3>
          <p className="import-copy">
            在本地生成一组合成目录记录，不访问任何外部模型站点。选择供应商后生成，再预览 diff，最后显式导入。
          </p>
          <Field label="发现来源供应商" htmlFor="discovery-provider">
            <select
              id="discovery-provider"
              className="select"
              value={providerId}
              onChange={(event) => {
                setProviderId(event.target.value);
                setDiscovered([]);
                setPreview(null);
                setDiscoveryRevision(null);
                setGoPreview(null);
              }}
            >
              {providerList.length === 0 ? <option value="">先添加供应商</option> : null}
              {providerList.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name}
                </option>
              ))}
            </select>
          </Field>
          <div className="form-actions">
            <ActionButton icon="search" onClick={runDiscovery} disabled={!providerId} busy={discover.busy}>
              生成合成记录
            </ActionButton>
            <ActionButton
              icon="file"
              onClick={runPreview}
              disabled={discovered.length === 0}
              busy={previewAction.busy}
            >
              预览 diff
            </ActionButton>
            <ActionButton
              variant="primary"
              icon="check"
              onClick={runImport}
              disabled={!preview || discoveryRevision === null || previewAction.busy}
              busy={importAction.busy}
            >
              导入
            </ActionButton>
          </div>
          {discovered.length > 0 ? (
            <p className="import-count num">{discovered.length} 条待预览记录 · 仅本地 Mock</p>
          ) : null}
        </section>

        <section className="import-card" aria-labelledby="catalog-title">
          <h3 id="catalog-title">models.dev JSON</h3>
          <p className="import-copy">
            粘贴保存的 models.dev 目录，或选择本地 JSON 文件。文件只在本机读取，不联网。目录必须带有依据的
            <code> retrieved_at </code>抓取时间（RFC3339），核心用它作为每个字段的 fetchedAt；缺失会被拒绝，
            页面不会自动伪造为当前时间。未知字段不会补默认值。
          </p>
          <label className="file-drop" htmlFor="modelsdev-file">
            <input
              id="modelsdev-file"
              type="file"
              accept="application/json,.json"
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (file) void loadCatalogFile(file);
              }}
            />
            <Icon name="upload" size={16} />
            选择本地 JSON 文件
          </label>
          <Field label="或粘贴 JSON" htmlFor="modelsdev-text">
            <textarea
              id="modelsdev-text"
              className="textarea textarea-code"
              rows={5}
              value={catalogText}
              placeholder='{"providers":{...}}'
              onChange={(event) => {
                setCatalogText(event.target.value);
                setCatalog(null);
                setCatalogPreview(null);
                setGoPreview(null);
              }}
              onBlur={() => {
                if (!catalogText.trim()) {
                  setCatalog(null);
                  return;
                }
                try {
                  const parsed = JSON.parse(catalogText) as unknown;
                  if (!isModelsDevCatalog(parsed)) {
                    setFileError("JSON 不是 models.dev 目录结构（需要 providers 映射或以供应商 ID 为键的对象）");
                    setCatalog(null);
                    return;
                  }
                  setFileError(null);
                  setCatalog(parsed);
                  setCatalogPreview(null);
                  setGoPreview(null);
                } catch {
                  setFileError("JSON 无法解析，请检查末尾逗号或引号");
                  setCatalog(null);
                }
              }}
            />
          </Field>
          {fileError ? <p className="field-error" role="alert">{fileError}</p> : null}
          {summary ? (
            <dl className="file-summary">
              <div>
                <dt>文件</dt>
                <dd className="num">{catalogFile ?? "粘贴内容"}</dd>
              </div>
              <div>
                <dt>供应商</dt>
                <dd className="num">{summary.providers}</dd>
              </div>
              <div>
                <dt>模型</dt>
                <dd className="num">{summary.models}</dd>
              </div>
              <div>
                <dt>抓取时间</dt>
                <dd className="num">{retrievedAt ?? "缺失 · 不可预览"}</dd>
              </div>
              <div>
                <dt>键</dt>
                <dd className="num">{[keys.slice(0, 3).join(", "), keys.length > 3 ? "…" : ""].join("")}</dd>
              </div>
            </dl>
          ) : null}
          <div className="form-actions">
            <ActionButton
              icon="file"
              onClick={previewCatalog}
              disabled={!catalog || !retrievedAt}
              busy={modelsDevAction.busy && catalogPreview === null}
            >
              预览导入
            </ActionButton>
            <ActionButton
              variant="primary"
              icon="check"
              onClick={importCatalog}
              disabled={!catalog || catalogPreview === null}
              busy={modelsDevAction.busy && catalogPreview !== null}
            >
              事务导入
            </ActionButton>
          </div>
          {catalogPreview === null && catalog ? (
            <p className="import-count">先预览，再导入。导入会使用预览返回的 revision。</p>
          ) : catalogPreview !== null ? (
            <p className="import-count num">预览绑定 revision {catalogPreview}；此期间若有其他写入，导入会被拒绝。</p>
          ) : null}
        </section>
      </div>

      {actionError ? (
        <ErrorNotice
          message={errorMessage(actionError)}
          recovery={errorRecovery(actionError)}
          onDismiss={() => {
            discover.clearError();
            previewAction.clearError();
            importAction.clearError();
            modelsDevAction.clearError();
          }}
        />
      ) : null}

      {notice ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {notice}
        </div>
      ) : null}

      {goPreview ? (
        <div className="diff-block">
          <div className="diff-head">
            <h3>核心预览差异</h3>
            <span className="num">
              revision {goPreview.revision} · 新增 {goPreview.summary.created} · 更新{" "}
              {goPreview.summary.updated} · 未变 {goPreview.summary.unchanged}
            </span>
          </div>
          {goPreview.changes.length === 0 ? (
            <div className="diff-empty">
              <p>无有效变更。批次内容与现有记录相同，或 manual 覆盖仍在生效。</p>
              <p className="panel-note">
                核心不会把未包含在本批次中的模型标为删除；导入是 upsert，且 manual 来源优先。
              </p>
            </div>
          ) : (
            <ul className="diff-list diff-list-models">
              {goPreview.changes.slice(0, 40).map((change) => (
                <li key={change.key} className={`diff-${change.state === "created" ? "added" : "changed"}`}>
                  <span className="diff-key num">{change.key}</span>
                  <span className="diff-field">
                    <Badge tone={change.state === "created" ? "success" : "accent"}>{change.stateLabel}</Badge>
                  </span>
                  <div className="diff-values">
                    {change.fields.length === 0 ? (
                      <span className="diff-field-row num">名称变化，能力字段未变</span>
                    ) : (
                      change.fields.map((field) => (
                        <span key={field.field} className="diff-field-row num">
                          {capLabel(field.field)} <span className="diff-before">{field.before}</span>（
                          {field.beforeSource}）→ <span className="diff-after">{field.after}</span>（
                          {field.afterSource}）
                        </span>
                      ))
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
          {goPreview.changes.length > 40 ? (
            <p className="table-footnote">仅显示前 40 个模型。</p>
          ) : null}
        </div>
      ) : null}

      <details className="preset-note">
        <summary>预设说明（{presetList.length}）</summary>
        <p>
          预设只用于填写供应商的协议和 Base URL。它们不包含 API Key，也不会测试连通性。真实发现需要显式授权；
          本地发现仅提供合成目录；上方公共目录获取是真实只读元数据。
        </p>
      </details>
    </div>
  );
}

function ChangeSnapshots({
  snapshots,
  onRollback,
}: {
  snapshots: ModelChangeSnapshot[];
  onRollback: () => void;
}) {
  const [openId, setOpenId] = useState<number | null>(null);
  const rollback = useAction<RollbackResult>();
  const [message, setMessage] = useState<string | null>(null);
  const [rollbackPreview,setRollbackPreview]=useState<{id:number; data:RollbackPreview} | null>(null);

  async function runRollback(id: number) {
    if(!rollbackPreview || rollbackPreview.id!==id) {
      try {setRollbackPreview({id,data:await api.models.rollbackPreview(id)});}catch(e){setMessage(errorMessage(e))}
      return;
    }
    if(rollbackPreview.data.conflicts.length) return;
    const result = await rollback.run(() => api.models.rollback(id,rollbackPreview.data.revision));
    if (result) {
      setMessage(`已回滚到 revision ${result.revision}，恢复 ${result.restored} 个模型、删除 ${result.deleted} 个模型，恢复 ${result.restoredProviders} 个供应商、删除 ${result.deletedProviders} 个供应商。`);
      setRollbackPreview(null);
      onRollback();
    }
  }

  return (
    <div className="snapshots">
      {message ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {message}
        </div>
      ) : null}
      {rollback.error ? (
        <ErrorNotice message={errorMessage(rollback.error)} recovery={errorRecovery(rollback.error)} onDismiss={rollback.clearError} />
      ) : null}
      {rollbackPreview ? <InfoNote tone="warn"><p>回滚预览 revision {rollbackPreview.data.revision}。将恢复该变更记录的完整实体；有后续修改则整批拒绝。</p>{rollbackPreview.data.conflicts.length ? <p>{rollbackPreview.data.conflicts.join("；")}</p> : <p>未检测到后续实体修改。再次点击同一回滚按钮确认。新增并发写入会使此预览失效。</p>}<details><summary>将恢复的模型与供应商</summary><pre>{JSON.stringify(rollbackPreview.data.detail,null,2)}</pre></details></InfoNote> : null}
      <ul className="snapshot-list">
        {snapshots.map((snapshot) => {
          const open = openId === snapshot.id;
          return (
            <li key={snapshot.id}>
              <div className="snapshot-row">
                <div className="snapshot-main">
                  <span className="snapshot-kind">{snapshot.kind || "CHANGED"}</span>
                  <span className="num snapshot-id">#{snapshot.id}</span>
                  <span className="num snapshot-rev">revision {snapshot.revision}</span>
                  <span className="num snapshot-time">{fmtDateTime(snapshot.createdAt)}</span>
                  {snapshot.rolledBack ? <Badge tone="warn">已回滚 → {snapshot.rollbackRevision ?? "?"}</Badge> : null}
                </div>
                <div className="snapshot-actions">
                  <button
                    type="button"
                    className="btn btn-quiet btn-sm"
                    aria-expanded={open}
                    onClick={() => setOpenId(open ? null : snapshot.id)}
                  >
                    <Icon name={open ? "chevronUp" : "chevronDown"} size={14} />
                    详情
                  </button>
                  <ActionButton
                    variant="quiet"
                    icon="refresh"
                    onClick={() => void runRollback(snapshot.id)}
                    busy={rollback.busy}
                    disabled={snapshot.rolledBack || (rollbackPreview?.id===snapshot.id && rollbackPreview.data.conflicts.length>0)}
                    title={snapshot.rolledBack ? "该变更已回滚" : "带 revision 校验回滚"}
                  >
                    {snapshot.rolledBack ? "已回滚" : rollbackPreview?.id===snapshot.id ? "确认回滚" : "预览回滚"}
                  </ActionButton>
                </div>
              </div>
              {open ? (
                <div className="snapshot-detail">
                  <p className="snapshot-summary">{snapshot.summary}</p>
                  <p className="panel-note">
                    基础 revision {snapshot.baseRevision} → {snapshot.revision}
                  </p>
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function OverrideDrawer({
  model,
  baseRevision,
  onClose,
  onSaved,
}: {
  model: ModelRecord;
  baseRevision: number;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [field, setField] = useState("context");
  const [kind, setKind] = useState<"number" | "boolean" | "null">("number");
  const [numberValue, setNumberValue] = useState("");
  const [booleanValue, setBooleanValue] = useState(false);
  const override = useAction<{ revision: number }>();
  const [message, setMessage] = useState<string | null>(null);
  const current = model.capabilities[field];
  const [displayName,setDisplayName]=useState(model.manualName ?? model.name);
  async function metadata(field:string,restoreAutomatic:boolean) {
    const result=await override.run(()=>api.models.metadata({providerId:model.providerId,id:model.id,field,restoreAutomatic,value:field==="displayName" ? displayName : undefined,revision:baseRevision}));
    if(result){onSaved();onClose();}
  }

  useEffect(() => {
    if (current) {
      if (typeof current.value === "number") {
        setKind("number");
        setNumberValue(String(current.value));
      } else if (typeof current.value === "boolean") {
        setKind("boolean");
        setBooleanValue(current.value);
      } else {
        setKind("null");
      }
    }
  }, [field, current]);

  const value: number | boolean | null =
    kind === "null" ? null : kind === "boolean" ? booleanValue : numberValue.trim() === "" ? null : Number(numberValue);
  const invalid = kind === "number" && (numberValue.trim() === "" || !Number.isFinite(Number(numberValue)) || Number(numberValue) < 0);

  async function submit() {
    const result = await override.run(() =>
      api.models.override({
        providerId: model.providerId,
        id: model.id,
        field,
        value,
        revision: baseRevision,
      }),
    );
    if (result) {
      setMessage(`覆盖已写入 revision ${result.revision}。`);
      onSaved();
      onClose();
    }
  }

  return (
    <div className="drawer-backdrop" role="presentation" onClick={onClose}>
      <section
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="override-title"
        onClick={(event) => event.stopPropagation()}
      >
        <header className="drawer-head">
          <div>
            <h2 id="override-title">字段覆盖</h2>
            <p className="num drawer-sub">
              {model.providerId}/{model.id}
            </p>
          </div>
          <button type="button" className="icon-btn" onClick={onClose} aria-label="关闭覆盖面板">
            <Icon name="close" size={18} />
          </button>
        </header>
        <div className="drawer-body">
          <p className="panel-note">编辑基线 revision {baseRevision}，刷新列表不会改变此草稿。保存后关闭；冲突时请关闭后重新读取。</p>
          <Field label="模型显示名" htmlFor="model-display-name"><input id="model-display-name" className="input" value={displayName} onChange={e=>setDisplayName(e.target.value)} /></Field>
          <div className="form-actions"><ActionButton onClick={()=>metadata("displayName",false)} disabled={!displayName.trim() || override.busy}>保存显示名</ActionButton><ActionButton onClick={()=>metadata("displayName",true)} disabled={override.busy}>恢复上游名称</ActionButton></div>
          <Field label="字段" htmlFor="override-field">
            <select
              id="override-field"
              className="select"
              value={field}
              onChange={(event) => setField(event.target.value)}
            >
              {CAPABILITY_FIELDS.map((key) => (
                <option key={key} value={key}>
                  {capLabel(key)}
                </option>
              ))}
            </select>
          </Field>
          <div className="override-current">
            <span>当前值</span>
            <span className="num">
              {current ? `${capabilityText(current.value)} · ${current.source}` : <Unknown />}
            </span>
            <span>{fmtDateTime(current?.fetchedAt)}</span>
          </div>
          <fieldset className="kind-picker">
            <legend>覆盖类型</legend>
            <label>
              <input type="radio" name="override-kind" checked={kind === "number"} onChange={() => setKind("number")} />
              数字
            </label>
            <label>
              <input type="radio" name="override-kind" checked={kind === "boolean"} onChange={() => setKind("boolean")} />
              布尔
            </label>
            <label>
              <input type="radio" name="override-kind" checked={kind === "null"} onChange={() => setKind("null")} />
              UNKNOWN（null）
            </label>
          </fieldset>
          {kind === "number" ? (
            <Field label="新数值" htmlFor="override-number" hint="非负整数。手动覆盖会压过目录来源。">
              <input
                id="override-number"
                className="input num"
                inputMode="numeric"
                value={numberValue}
                onChange={(event) => setNumberValue(event.target.value.replace(/[^\d]/g, ""))}
                aria-invalid={invalid}
              />
            </Field>
          ) : null}
          {kind === "boolean" ? (
            <label className="toggle">
              <input type="checkbox" checked={booleanValue} onChange={(event) => setBooleanValue(event.target.checked)} />
              <span className="toggle-track" aria-hidden="true">
                <span className="toggle-knob" />
              </span>
              <span className="toggle-copy">
                <span className="toggle-label">{booleanValue ? "true · 支持" : "false · 不支持"}</span>
                <span className="toggle-hint">false 与 UNKNOWN 不同，会明确记录不支持。</span>
              </span>
            </label>
          ) : null}
          {kind === "null" ? (
            <InfoNote tone="warn">
              <p>写入 null 会把该字段标记为手动 UNKNOWN，目录更新不会覆盖它。点击“恢复自动来源”可取消人工覆盖。</p>
            </InfoNote>
          ) : null}
          {override.error ? (
            <ErrorNotice message={errorMessage(override.error)} recovery={errorRecovery(override.error)} onDismiss={override.clearError} />
          ) : null}
          {message ? (
            <div className="save-flash" role="status">
              <Icon name="check" size={16} />
              {message}
            </div>
          ) : null}
        </div>
        <footer className="drawer-foot">
          <ActionButton variant="primary" icon="check" onClick={submit} disabled={invalid} busy={override.busy}>
            提交覆盖
          </ActionButton>
          <ActionButton variant="quiet" onClick={()=>metadata(field,true)} disabled={override.busy}>恢复自动来源</ActionButton>
          <ActionButton variant="quiet" onClick={onClose}>
            关闭
          </ActionButton>
        </footer>
      </section>
    </div>
  );
}
