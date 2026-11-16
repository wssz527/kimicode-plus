import { useEffect, useMemo, useState } from "react";
import { api, type PluginActivity, type PluginPreview, type PluginRecord } from "../api";
import { Icon } from "./Icon";
import { ActionButton, Badge, EmptyState, ErrorNotice, Field, InfoNote, Panel, SkeletonRows, StateBadge } from "./ui";
import { PageError } from "../AppShell";
import { errorMessage, errorRecovery, useAction, useResource } from "../lib/useResource";
import { fmtDateTime, stateLabel, stateTone } from "../lib/format";

/** Capability copy in user language. A plugin page that shows `metrics:read` to a
 * non-developer is not explaining what the plugin will actually do. */
const CAPABILITY_LABELS: Record<string, { title: string; detail: string }> = {
  "metrics:read": { title: "读取缓存与用量指标", detail: "缓存命中、Token 速度、会话用量汇总；不含会话正文。" },
  "sessions:read": { title: "读取会话列表与内容", detail: "可看到你的会话标题和消息内容。" },
  "sessions:send": { title: "向会话发送消息", detail: "会以插件身份向指定会话发送消息，并写入活动记录。" },
  "config:read": { title: "读取 Kimi Code 配置", detail: "读取本机 Kimi Code 的配置项。" },
  "config:write": { title: "修改 Kimi Code 配置", detail: "可以直接改动本机 Kimi Code 配置，每次修改都会记录。" },
  "ui:sidebar": { title: "在侧栏显示内容", detail: "在官方侧栏底部挂载插件自己的界面。" },
  "ui:settings": { title: "在插件页提供设置", detail: "在插件详情里增加一个设置子页。" },
  "ui:thinking": { title: "装饰思考内容块", detail: "可以读取并改写思考块的显示。" },
  "ui:message": { title: "装饰消息块", detail: "可以读取并改写消息块的显示。" },
  storage: { title: "使用插件私有存储", detail: "只有该插件可读写的本地键值存储。" },
};

function capabilityLabel(name: string): { title: string; detail: string } {
  if (name.startsWith("net:")) {
    const host = name.slice(4);
    return { title: `访问 ${host}`, detail: `经核心代理访问 ${host} 的网络请求；其他主机一律拒绝。` };
  }
  if (name.startsWith("credentials:")) {
    const ref = name.slice("credentials:".length);
    return { title: `使用凭据 ${ref}`, detail: "在出站请求中由核心注入凭据明文，插件本身拿不到明文。" };
  }
  return CAPABILITY_LABELS[name] ?? { title: name, detail: "未识别的能力，请核对插件清单。" };
}

const ACTIVITY_LABELS: Record<string, string> = {
  INSTALL: "安装",
  ENABLE: "启用",
  DISABLE: "停用",
  UNINSTALL: "卸载",
  UPGRADE: "升级",
  RESTORE: "恢复",
  ERROR: "插件日志",
  SESSIONS_SEND: "发送会话",
  CONFIG_WRITE: "修改配置",
  NETWORK: "网络请求",
  CORRUPT: "异常停用",
  VERSION_BLOCKED: "版本门禁",
  SAFE_MODE: "安全模式",
};

const PLAIN_STATE: Record<string, string> = {
  ENABLED: "已启用",
  DISABLED: "已停用",
  BLOCKED: "已被安全模式停止",
  FAULTED: "出错",
  CORRUPT: "文件异常",
  INCOMPATIBLE: "未在此版本验证",
};

export interface PluginSettingsProps {
  /** Rendered inside the settings dialog, where the page header is the dialog's. */
  embedded?: boolean;
}

export function PluginSettings({ embedded = false }: PluginSettingsProps) {
  const plugins = useResource("plugins/list", () => api.plugins.list());
  const [selected, setSelected] = useState<string | null>(null);
  const [flash, setFlash] = useState<string | null>(null);
  const [installOpen, setInstallOpen] = useState(false);
  const [installPath, setInstallPath] = useState("");
  const [installError, setInstallError] = useState<string | null>(null);
  const [preview, setPreview] = useState<PluginPreview | null>(null);
  const [granted, setGranted] = useState<string[]>([]);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [confirmForce, setConfirmForce] = useState(false);
  const action = useAction<PluginRecord | { id: string; uninstalled: boolean }>();

  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(null), 3600);
    return () => clearTimeout(timer);
  }, [flash]);

  const items = plugins.data?.items ?? [];
  const current = useMemo(() => items.find(item => item.id === selected) ?? null, [items, selected]);
  const activity = useResource<PluginActivity[]>(
    `plugins/activity/${selected ?? ""}`,
    async () => (selected ? (await api.plugins.activity(selected)).items : []),
    { enabled: Boolean(selected) },
  );

  async function run(id: string, kind: "enable" | "disable" | "uninstall") {
    if (kind === "uninstall") {
      const done = await action.run(() => api.plugins.uninstall(id));
      if (done) {
        setSelected(null);
        setFlash("插件已卸载。卸载后的预装插件不会自动恢复。");
        plugins.reload();
      }
      return;
    }
    const result = await action.run(() => (kind === "enable" ? api.plugins.enable(id) : api.plugins.disable(id)));
    if (result && "state" in result) {
      setFlash(`「${result.name || result.id}」现在${PLAIN_STATE[result.state] ?? stateLabel(result.state)}。`);
      plugins.reload();
      activity.reload();
    }
  }

  function closeInstall() {
    setInstallOpen(false);
    setInstallPath("");
    setPreview(null);
    setGranted([]);
    setInstallError(null);
  }

  // Validation is a separate, side-effect-free step: the user sees the manifest
  // and the capability list before anything is written to disk, and the install
  // carries exactly the list they confirmed.
  async function inspect() {
    setInstallError(null);
    setPreview(null);
    if (!installPath.trim()) {
      setInstallError("请填写插件文件夹或 zip 的完整路径。");
      return;
    }
    setPreviewBusy(true);
    try {
      const result = await api.plugins.preview(installPath.trim());
      setPreview(result);
      setGranted(result.capabilities);
    } catch (error) {
      setInstallError(errorMessage(error) || "无法读取这个插件包。");
    } finally {
      setPreviewBusy(false);
    }
  }

  async function install() {
    if (!preview) return;
    setInstallError(null);
    const installed = (await action.run(() => api.plugins.install(installPath.trim(), granted))) as PluginRecord | null;
    if (installed) {
      closeInstall();
      setSelected(installed.id);
      setFlash(`已安装「${installed.name}」。它保持停用，确认能力后再启用。`);
      plugins.reload();
    } else {
      setInstallError(errorMessage(action.error) || "安装未完成。");
    }
  }

  async function forceEnable(id: string) {
    const result = await action.run(() => api.plugins.forceEnable(id));
    if (result && "state" in result) {
      setConfirmForce(false);
      setFlash(`「${result.name || result.id}」已强制启用。它未在当前宿主版本验证。`);
      plugins.reload();
      activity.reload();
    }
  }

  async function restore(id: string) {
    const result = await action.run(() => api.plugins.restore(id));
    if (result && "state" in result) {
      setFlash(`预装插件「${result.name || result.id}」已恢复，保持停用。`);
      plugins.reload();
      activity.reload();
    }
  }

  const safeMode = Boolean(plugins.data?.safeMode);

  if (current) {
    const capabilities = current.capabilities ?? [];
    const ungranted = (current.declaredCapabilities ?? []).filter(name => !capabilities.includes(name));
    return (
      <div className={embedded ? "ekds-plugins" : "page"}>
        <div className="plugins-detail-head">
          <ActionButton variant="quiet" icon="arrowRight" onClick={() => setSelected(null)}>
            返回插件列表
          </ActionButton>
          <div className="plugins-detail-title">
            <h2>{current.name || current.id}</h2>
            <span className="num">{current.id} · v{current.version}</span>
          </div>
          <StateBadge state={current.state} tone={stateTone(current.state)} label={PLAIN_STATE[current.state] ?? stateLabel(current.state)} />
        </div>

        {flash ? (
          <div className="save-flash" role="status">
            <Icon name="check" size={16} />
            {flash}
          </div>
        ) : null}
        {action.error ? (
          <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
        ) : null}

        {current.error ? <ErrorNotice message={current.error} recovery="插件已停用。修正文件或更换插件后可以重新启用。" /> : null}

        <div className="plugins-detail-grid">
          <Panel title="能力">
            {capabilities.length === 0 ? (
              <p className="panel-note">这个插件没有获得任何能力，只能显示自己的界面。</p>
            ) : (
              <ul className="capability-list">
                {capabilities.map(name => {
                  const label = capabilityLabel(name);
                  return (
                    <li key={name}>
                      <Icon name="check" size={14} />
                      <div>
                        <strong>{label.title}</strong>
                        <p>{label.detail}</p>
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
            {ungranted.length > 0 ? (
              <InfoNote tone="warn">
                <p>
                  清单还申请了 {ungranted.map(name => capabilityLabel(name).title).join("、")}，但你没有授予。插件无法使用这些能力。
                </p>
              </InfoNote>
            ) : null}
          </Panel>

          <Panel title="信息">
            <dl className="rail-facts">
              <div>
                <dt>来源</dt>
                <dd>{current.source === "BUNDLED" ? "预装" : "本地安装"}</dd>
              </div>
              <div>
                <dt>许可证</dt>
                <dd>{current.license || "未声明"}</dd>
              </div>
              <div>
                <dt>适用宿主</dt>
                <dd className="num">{(current.hostVersions ?? []).join(", ") || "未声明"}</dd>
              </div>
              <div>
                <dt>版本检验</dt>
                <dd>{current.compatible ? "通过" : "未在此版本验证"}</dd>
              </div>
              {current.updatedAt ? (
                <div>
                  <dt>最近变化</dt>
                  <dd className="num">{fmtDateTime(current.updatedAt)}</dd>
                </div>
              ) : null}
            </dl>
            {current.upstream ? <p className="panel-note">上游：{current.upstream}</p> : null}
          </Panel>

          <Panel title="操作">
            <div className="plugins-detail-actions">
              {current.enabled ? (
                <ActionButton icon="power" onClick={() => void run(current.id, "disable")} busy={action.busy}>
                  停用插件
                </ActionButton>
              ) : (
                <ActionButton
                  variant="primary"
                  icon="power"
                  onClick={() => void run(current.id, "enable")}
                  busy={action.busy}
                  disabled={safeMode}
                >
                  启用插件
                </ActionButton>
              )}
              {!current.enabled && !current.compatible ? (
                <ActionButton icon="alert" onClick={() => setConfirmForce(true)} busy={action.busy} disabled={safeMode}>
                  强制启用
                </ActionButton>
              ) : null}
              <ActionButton variant="danger" icon="trash" onClick={() => void run(current.id, "uninstall")} busy={action.busy}>
                卸载
              </ActionButton>
            </div>
            {!current.enabled && !current.compatible ? (
              <InfoNote tone="warn">
                <p>这个插件没有声明当前宿主版本，默认不会启用。强制启用后它会以未验证状态运行。</p>
              </InfoNote>
            ) : null}
            {confirmForce ? (
              <div className="plugin-confirm" role="dialog" aria-modal="true" aria-label="确认强制启用">
                <p>
                  强制启用「{current.name || current.id}」意味着让一个没有在当前宿主版本验证过的插件运行。它可能读到你的会话内容。确认继续？
                </p>
                <div className="inline-actions">
                  <ActionButton variant="danger" icon="check" onClick={() => void forceEnable(current.id)} busy={action.busy}>
                    确认强制启用
                  </ActionButton>
                  <ActionButton variant="quiet" onClick={() => setConfirmForce(false)}>
                    取消
                  </ActionButton>
                </div>
              </div>
            ) : null}
            <p className="panel-note">卸载会删除插件文件和它的私有存储。预装插件卸载后不会自动恢复，可以用下方的"恢复预装插件"重新安装。</p>
          </Panel>
          {current.source === "BUNDLED" ? (
            <Panel title="预装副本">
              <p className="panel-note">这个插件随应用一起提供。卸载后需要手动恢复，不会在下次启动时自动回到列表。</p>
              <ActionButton icon="refresh" onClick={() => void restore(current.id)} busy={action.busy}>
                恢复预装插件
              </ActionButton>
            </Panel>
          ) : null}
        </div>

        <Panel title="活动记录" aside={<span className="num">{activity.data?.length ?? 0}</span>}>
          {activity.loading ? (
            <SkeletonRows rows={3} cols={3} />
          ) : activity.error ? (
            <PageError error={activity.error} />
          ) : (activity.data ?? []).length === 0 ? (
            <p className="panel-note">还没有记录。启用、停用和插件发起的动作都会记录在这里。</p>
          ) : (
            <ul className="activity-list">
              {(activity.data ?? []).map((item: PluginActivity) => (
                <li key={item.id}>
                  <span className="num">{fmtDateTime(item.at)}</span>
                  <Badge tone="neutral">{ACTIVITY_LABELS[item.kind] ?? item.kind}</Badge>
                  <span>{item.detail}</span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    );
  }

  return (
    <div className={embedded ? "ekds-plugins" : "page"}>
      <div className="plugins-list-head">
        <div>
          <h2>插件</h2>
          <p>插件运行在当前界面里，可以读取你看到的会话内容。只安装你信任的插件。</p>
        </div>
        <div className="plugins-list-actions">
          <ActionButton icon="refresh" onClick={plugins.reload} busy={plugins.loading}>
            刷新
          </ActionButton>
          <ActionButton variant="primary" icon="plus" onClick={() => setInstallOpen(open => !open)} disabled={safeMode}>
            安装本地插件
          </ActionButton>
        </div>
      </div>

      {flash ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {flash}
        </div>
      ) : null}

      {safeMode ? (
        <InfoNote tone="warn">
          <p>安全模式已开启。所有插件都已停用，安装与启用都会被拒绝。关闭安全模式后可以重新启用。</p>
        </InfoNote>
      ) : null}

      {installOpen ? (
        <Panel title={preview ? "确认能力并安装" : "从本地安装"}>
          {preview ? (
            <>
              <dl className="rail-facts">
                <div>
                  <dt>插件</dt>
                  <dd>{preview.name || preview.id}</dd>
                </div>
                <div>
                  <dt>版本</dt>
                  <dd className="num">{preview.version}</dd>
                </div>
                <div>
                  <dt>作者</dt>
                  <dd>{preview.author || "未声明"}</dd>
                </div>
                <div>
                  <dt>许可证</dt>
                  <dd>{preview.license || "未声明"}</dd>
                </div>
                <div>
                  <dt>适用宿主</dt>
                  <dd className="num">{preview.hostVersions.join(", ") || "未声明"}</dd>
                </div>
              </dl>
              {preview.installed ? (
                <InfoNote tone="warn">
                  <p>这个插件已经安装过。继续安装会替换它的文件，但保留你当前授予的能力和启用状态。</p>
                </InfoNote>
              ) : null}
              {!preview.compatible ? (
                <InfoNote tone="warn">
                  <p>插件没有声明当前宿主版本。安装后需要"强制启用"才能运行，请先确认它可信。</p>
                </InfoNote>
              ) : null}
              {preview.capabilities.length === 0 ? (
                <p className="panel-note">这个插件没有申请任何能力，只能显示自己的界面。</p>
              ) : (
                <>
                  <p className="panel-note">勾选你愿意授予的能力。未勾选的能力插件无法使用。</p>
                  <ul className="capability-list">
                    {preview.capabilities.map(name => {
                      const label = capabilityLabel(name);
                      return (
                        <li key={name}>
                          <label className="capability-choice">
                            <input
                              type="checkbox"
                              checked={granted.includes(name)}
                              onChange={event =>
                                setGranted(current =>
                                  event.target.checked ? [...current, name] : current.filter(item => item !== name),
                                )
                              }
                            />
                            <span>
                              <strong>{label.title}</strong>
                              <span className="capability-detail">{label.detail}</span>
                            </span>
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                  {granted.length < preview.capabilities.length ? (
                    <InfoNote tone="warn">
                      <p>你只授予了 {granted.length} / {preview.capabilities.length} 项能力。</p>
                    </InfoNote>
                  ) : null}
                </>
              )}
            </>
          ) : (
            <Field label="插件文件夹或 zip 路径" htmlFor="plugin-install-path" hint="只从你选择的本地位置安装。安装后插件保持停用，确认能力后再启用。">
              <input
                id="plugin-install-path"
                className="input"
                value={installPath}
                onChange={event => setInstallPath(event.target.value)}
                onKeyDown={event => {
                  if (event.key === "Enter") void inspect();
                }}
                placeholder="/Users/你/插件目录 或 插件.zip"
              />
            </Field>
          )}
          {installError ? <ErrorNotice message={installError} onDismiss={() => setInstallError(null)} /> : null}
          <div className="inline-actions">
            {preview ? (
              <>
                <ActionButton variant="primary" icon="check" onClick={() => void install()} busy={action.busy}>
                  授予 {granted.length} 项能力并安装
                </ActionButton>
                <ActionButton variant="quiet" onClick={() => setPreview(null)}>
                  重新选择
                </ActionButton>
              </>
            ) : (
              <ActionButton variant="primary" icon="check" onClick={() => void inspect()} busy={previewBusy}>
                检查插件包
              </ActionButton>
            )}
            <ActionButton variant="quiet" onClick={closeInstall}>
              取消
            </ActionButton>
          </div>
        </Panel>
      ) : null}

      {plugins.loading ? (
        <SkeletonRows rows={4} cols={4} />
      ) : plugins.error ? (
        <PageError error={plugins.error} />
      ) : items.length === 0 ? (
        <EmptyState icon="plugins" title="还没有插件">
          安装一个本地插件，或检查内置示例插件是否被卸载。核心功能不依赖任何插件。
        </EmptyState>
      ) : (
        <ul className="plugin-list">
          {items.map(plugin => (
            <li key={plugin.id} className={plugin.enabled ? "plugin" : "plugin muted"}>
              <button type="button" className="plugin-open" onClick={() => setSelected(plugin.id)}>
                <span className="plugin-glyph" aria-hidden="true">
                  <Icon name={plugin.state === "CORRUPT" || plugin.state === "FAULTED" ? "alert" : "plugins"} size={17} />
                </span>
                <span className="plugin-line">
                  <strong>{plugin.name || plugin.id}</strong>
                  <span className="num">
                    v{plugin.version} · {plugin.source === "BUNDLED" ? "预装" : "本地"}
                  </span>
                </span>
                <StateBadge state={plugin.state} tone={stateTone(plugin.state)} label={PLAIN_STATE[plugin.state] ?? stateLabel(plugin.state)} />
                <span className="plugin-count num">{(plugin.capabilities ?? []).length} 项能力</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
