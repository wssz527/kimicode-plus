import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api, ApiError, type KctMigrationResult, type MonitorMigrationResult, type PluginActivity, type PluginPlatformLimitation, type PluginPreview, type PluginRecord, type PluginUpdateItem, type PluginUpdateStatus, type PluginUpstream } from "../api";
import { pluginHostBridge, syncPluginHost } from "../plugin-runtime/host";
import type { NativeHost } from "../native";
import { Icon } from "./Icon";
import { ActionButton, Badge, EmptyState, ErrorNotice, Field, InfoNote, Panel, SkeletonRows, StateBadge, Toggle } from "./ui";
import { PluginCredentials } from "./PluginCredentials";
import { requestWindowsReopen } from "./WindowsAttachStatus";
import { PageError } from "../AppShell";
import { errorMessage, errorRecovery, useAction, useResource } from "../lib/useResource";
import { fmtBytes, fmtDateTime, stateLabel, stateTone } from "../lib/format";

/** Capability copy in user language. A plugin page that shows `metrics:read` to a
 * non-developer is not explaining what the plugin will actually do. */
const CAPABILITY_LABELS: Record<string, { title: string; detail: string }> = {
  "metrics:read": { title: "读取缓存与用量指标", detail: "缓存命中、Token 速度、会话用量汇总；不含会话正文。" },
  "sessions:read": { title: "读取会话列表与内容", detail: "可看到你的会话标题和消息内容。" },
  "sessions:send": { title: "向会话发送消息", detail: "会以插件身份向指定会话发送消息，并写入活动记录。" },
  "config:read": { title: "读取 Kimi Code 配置", detail: "读取本机 Kimi Code 的配置项。" },
  "config:write": { title: "修改 Kimi Code 配置", detail: "可以直接改动本机 Kimi Code 配置，每次修改都会记录。" },
  "ui:sidebar": { title: "在侧栏显示内容", detail: "在官方侧栏底部挂载插件自己的界面。" },
  "ui:settings": { title: "在增强插件页提供设置", detail: "在增强插件详情里增加一个设置子页。" },
  "ui:thinking": { title: "装饰思考内容块", detail: "可以读取并改写思考块的显示。" },
  "ui:message": { title: "装饰消息块", detail: "可以读取并改写消息块的显示。" },
  "ui:entry": { title: "在会话信息中显示入口", detail: "在会话信息的「可打开」和右侧面板列表添加快捷入口。" },
  "ui:sidebar-nav": { title: "在左侧栏显示入口", detail: "在左侧栏「新建会话」下方添加导航入口。" },
  "ui:main-area": { title: "在主聊天区打开界面", detail: "在主聊天区打开增强插件专属分栏或整页界面。" },
  "ui:right-panel": { title: "在右侧面板打开标签页", detail: "在右侧面板打开增强插件标签页。" },
  "ui:layout": { title: "调整宿主布局", detail: "可为插件界面调整侧栏和面板布局。" },
  "sessions:approve": { title: "处理会话审批", detail: "可向等待审批的会话提交批准或拒绝决定。" },
  "sessions:abort": { title: "停止会话运行", detail: "可中断指定会话的当前运行。" },
  "sessions:history": { title: "读取会话历史与审批记录", detail: "可翻页读取消息和审批记录。" },
  attachments: { title: "选择和发送附件", detail: "可选择本地文件、暂存附件并将它们发送至会话。" },
  "browser:view": { title: "控制内置浏览器视图", detail: "可在自己的浏览器视图中打开网页、导航并读取页面内容。" },
  "planner:run": { title: "运行规划器", detail: "可创建和管理规划线程、发送提示词并处理其审批。" },
  handoff: { title: "向会话交接内容", detail: "可把内容提交给已有会话或创建新的会话。" },
  commands: { title: "注册和调用增强插件命令", detail: "可注册自己的命令并调用其他增强插件公开的命令。" },
  "models:read": { title: "读取可用模型列表", detail: "读取官方已配置的模型与推理强度选项。" },
  "schedule:manage": { title: "管理定时任务", detail: "创建、启停、立即运行和删除定时任务，查看运行记录与未读状态。" },
  storage: { title: "使用增强插件私有存储", detail: "只有该增强插件可读写的本地键值存储。" },
};

export function capabilityLabel(name: string): { title: string; detail: string } {
  if (name === "net:https") {
    return { title: "访问任意公网 HTTPS 服务", detail: "经核心代理访问用户配置的公网 HTTPS 地址；仅限加密连接，仍拦截本机与内网地址。" };
  }
  if (name.startsWith("net:")) {
    const host = name.slice(4);
    if (host === "localhost" || host === "127.0.0.1" || host === "::1") {
      return { title: "访问本机服务", detail: `经核心代理请求 ${host} 上的服务；其他主机一律拒绝。` };
    }
    return { title: `访问 ${host}`, detail: `经核心代理访问 ${host} 的网络请求；其他主机一律拒绝。` };
  }
  if (name.startsWith("credentials:")) {
    const ref = name.slice("credentials:".length);
    return { title: `使用凭据 ${ref}`, detail: "在出站请求中由核心注入凭据明文，增强插件本身拿不到明文。" };
  }
  return CAPABILITY_LABELS[name] ?? { title: name, detail: "未识别的能力，请核对增强插件清单。" };
}

function CapabilityTitle({ name, privileged }: { name: string; privileged: readonly string[] }) {
  return (
    <span className="plugin-capability-title">
      <strong>{capabilityLabel(name).title}</strong>
      {privileged.includes(name) ? <Badge tone="warn">高权限</Badge> : null}
    </span>
  );
}

function CapabilityPlatformNotes({ name, limitations }: { name: string; limitations?: PluginPlatformLimitation[] }) {
  return limitations?.filter(item => item.capability === name).map(item => (
    <span className="capability-detail" key={item.feature}>
      <Badge tone="warn">需重新打开 Kimi Code</Badge> {item.message}
    </span>
  ));
}

function WindowsPluginReopen({ host }: { host: NativeHost | null }) {
  const action = useAction<{ reopening: boolean }>();
  const [reopening, setReopening] = useState(false);
  useEffect(() => {
    if (!reopening) return;
    const timeout = window.setTimeout(() => setReopening(false), 25_000);
    return () => window.clearTimeout(timeout);
  }, [reopening]);
  async function reopen() {
    const result = await action.run(() => requestWindowsReopen(host));
    if (result?.reopening) setReopening(true);
  }
  return <InfoNote tone="warn">
    <p>需要从 KimiCode+ 入口重新打开 Kimi Code。会话运行中请先等它结束。</p>
    <ActionButton onClick={() => void reopen()} busy={action.busy} disabled={reopening}>
      {reopening ? "正在重新打开…" : "立即重新打开"}
    </ActionButton>
    {action.error ? <p role="alert">{errorMessage(action.error)}</p> : null}
  </InfoNote>;
}

function PluginUpstreamInfo({ upstream }: { upstream: PluginUpstream & { upstream?: string } }) {
  const { upstreamRepo, upstreamUrl, upstreamCommit, upstreamLicense } = upstream;
  if (!upstreamRepo && !upstreamUrl && !upstreamCommit && !upstreamLicense) {
    return upstream.upstream ? <p className="panel-note">上游仓库及许可信息 · {upstream.upstream}</p> : null;
  }
  return (
    <div className="plugin-upstream">
      {upstreamRepo || upstreamUrl ? <p className="panel-note">基于上游开源仓库改编适配</p> : null}
      <dl className="rail-facts">
        {upstreamRepo || upstreamUrl ? (
          <div>
            <dt>上游仓库</dt>
            <dd>
              {upstreamUrl ? <a href={upstreamUrl} target="_blank" rel="noopener noreferrer">{upstreamRepo || upstreamUrl}</a> : upstreamRepo}
            </dd>
          </div>
        ) : null}
        {upstreamCommit ? <div><dt>基线 commit</dt><dd><code title={upstreamCommit}>{upstreamCommit.slice(0, 8)}</code></dd></div> : null}
        {upstreamLicense ? <div><dt>上游许可证</dt><dd>{upstreamLicense}</dd></div> : null}
      </dl>
    </div>
  );
}

function PluginLegalFiles({ plugin }: { plugin: PluginRecord }) {
  const [open, setOpen] = useState<"license" | "notice" | null>(null);
  const regionId = useId();
  // The version and update timestamp are part of the cache identity: after a hot
  // update the same plugin id and install time describe different files, and an
  // expanded LICENSE must show the new text instead of the previous one.
  const revision = `${plugin.version}/${plugin.updatedAt ?? ""}`;
  const legal = useResource(`plugins/legal/${plugin.id}/${plugin.installedAt ?? ""}/${revision}`, () => api.pluginLegal(plugin.id), { enabled: open !== null });
  const notice = open === "notice";
  const text = notice ? legal.data?.noticeText : legal.data?.licenseText;
  const truncated = notice ? legal.data?.noticeTruncated : legal.data?.licenseTruncated;
  const filename = notice ? legal.data?.noticeFile || "NOTICE" : "LICENSE";
  return (
    <div className="plugin-legal">
      <div className="inline-actions">
        <ActionButton aria-expanded={open === "license"} aria-controls={regionId} onClick={() => setOpen(open === "license" ? null : "license")}>
          查看 LICENSE
        </ActionButton>
        {plugin.hasNotice ? (
          <ActionButton aria-expanded={notice} aria-controls={regionId} onClick={() => setOpen(notice ? null : "notice")}>
            查看 NOTICE
          </ActionButton>
        ) : null}
      </div>
      <div id={regionId} hidden={!open}>
        {open ? (
          legal.loading ? <SkeletonRows rows={2} cols={1} /> : legal.error ? (
            <>
              <ErrorNotice message={errorMessage(legal.error)} />
              <ActionButton icon="refresh" onClick={legal.reload}>重新读取</ActionButton>
            </>
          ) : text ? (
            <>
              {truncated ? <p className="panel-note">{filename} 超过 128 KiB，此处显示文件开头。</p> : null}
              <pre className="plugin-legal-text" tabIndex={0} aria-label={filename}>{text}</pre>
            </>
          ) : <p className="panel-note">{filename} 没有可显示的内容。</p>
        ) : null}
      </div>
    </div>
  );
}

/** The bundled KCT adapter, whose enable is gated by the standalone handover. */
const KCT_PLUGIN_ID = "kimicode-plus.kct";

/** The bundled Monitor panel, gated by the standalone desktop patch handover. */
const MONITOR_PLUGIN_ID = "kimicode-plus.monitor";

const PLANNER_PLUGIN_ID = "kimicode-plus.planner";

function canQuickToggle(plugin: PluginRecord): boolean {
  return plugin.state !== "UNINSTALLED" && plugin.state !== "CORRUPT" && Boolean(plugin.compatible || plugin.enabled);
}

function isMigrationGate(error: unknown): boolean {
  return error instanceof ApiError && error.code === "PLUGIN_MIGRATION_REQUIRED";
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
  ENABLE_BLOCKED: "门禁拦截",
  SAFE_MODE: "安全模式",
  CREDENTIAL: "凭据",
};

const PLAIN_STATE: Record<string, string> = {
  ENABLED: "已启用",
  DISABLED: "已停用",
  BLOCKED: "已阻止启用",
  FAULTED: "出错",
  CORRUPT: "文件异常",
  INCOMPATIBLE: "未在此版本验证",
  UNINSTALLED: "已卸载",
};

const FAULT_REASONS: Record<string, string> = {
  PLUGIN_ACTIVATION_FAILED: "增强插件启动失败",
  PLUGIN_MOUNT_FAILED: "增强插件界面挂载失败",
  PLUGIN_DECORATE_FAILED: "增强插件装饰宿主内容失败",
  PLUGIN_EVENT_FAILED: "增强插件处理事件失败",
  PLUGIN_EVENT_POLL_FAILED: "增强插件事件订阅中断",
  PLUGIN_SETTINGS_FAILED: "增强插件设置页加载失败",
  RENDERER_ERROR: "增强插件运行时报错",
};

function describeFault(reason: string): { title: string; detail: string; code: string | null } {
  const trimmed = reason.trim();
  const match = /^([A-Z][A-Z0-9_]{2,}):\s*([\s\S]*)$/.exec(trimmed);
  if (!match) {
    return { title: trimmed || "增强插件已停止", detail: trimmed, code: null };
  }
  const code = match[1] ?? "";
  const detail = (match[2] ?? "").trim() || trimmed;
  return {
    title: FAULT_REASONS[code] ?? "增强插件已停止",
    detail,
    code: code || null,
  };
}

function PluginFaultNotice({ reason }: { reason: string }) {
  const fault = describeFault(reason);
  return (
    <div className="notice notice-error" role="alert">
      <Icon name="alert" size={18} />
      <div className="notice-copy">
        <strong>{fault.title}</strong>
        {fault.detail && fault.detail !== fault.title ? <p>{fault.detail}</p> : null}
        <p className="notice-recovery">增强插件已停用。修正文件或更换增强插件后可以重新启用。</p>
        {fault.code ? (
          <details className="notice-diagnostics">
            <summary>诊断信息</summary>
            <code>{reason}</code>
          </details>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Hosts a plugin's own settings module. The page owns the container and the
 * lifetime: switching detail, disabling, uninstalling or leaving the panel all
 * dispose the renderer-side release, which empties the container.
 *
 * The bridge exists only inside the native host. In a plain web page there is no
 * plugin runtime, so the panel explains that instead of rendering nothing.
 */
function PluginSettingsPanel({ plugin }: { plugin: PluginRecord }) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "unavailable" | "failed">("loading");
  const [message, setMessage] = useState("");

  useEffect(() => {
    const bridge = pluginHostBridge();
    const element = hostRef.current;
    if (!bridge || !element) {
      setState("unavailable");
      return;
    }
    let released = false;
    let dispose: (() => void) | null = null;
    setState("loading");
    setMessage("");
    bridge
      .mountSettings(plugin.id, element)
      .then(release => {
        if (released) {
          release();
          return;
        }
        dispose = release;
        setState("ready");
      })
      .catch(error => {
        if (released) return;
        setState("failed");
        setMessage(error instanceof Error ? error.message : String(error));
      });
    return () => {
      released = true;
      dispose?.();
    };
  }, [plugin.id, plugin.version, plugin.updatedAt]);

  return (
    <section className="plugin-settings-section" aria-label="增强插件设置">
      <div className="plugin-section-head">
        <h3>设置</h3>
      </div>
      {state === "unavailable" ? (
        <p className="panel-note">增强插件设置只在 Kimi Code 宿主内可用。当前页面没有运行增强插件运行时。</p>
      ) : state === "loading" ? (
        <SkeletonRows rows={2} cols={1} />
      ) : state === "failed" ? (
        <ErrorNotice message={message || "增强插件设置页加载失败。"} recovery="停用再启用增强插件后重试；仍然失败时检查增强插件文件是否为最新版本。" />
      ) : null}
      <div className="ekdp-settings-mount" ref={hostRef} hidden={state !== "ready"} />
    </section>
  );
}

/**
 * The one-time handover from the standalone KCT extension. It is a core route
 * rather than a plugin route: at the moment the page needs it the adapter is
 * still disabled. The panel explains what will be stopped, copies only the two
 * public settings, and keeps the loader untouched until the user confirms.
 */
function KctMigrationPanel({ show, onMigrated }: { show: boolean; onMigrated?: () => void }) {
  const report = useResource("plugins/kct/migration", () => api.plugins.kctMigration(), { enabled: show });
  const action = useAction<KctMigrationResult>();
  const [result, setResult] = useState<KctMigrationResult | null>(null);

  if (!show) return null;
  const state = result?.report ?? report.data ?? null;
  const needsConfirmation = Boolean(state?.needsConfirmation);
  const migrated = Boolean(state?.migrated);

  async function confirm() {
    const done = await action.run(() => api.plugins.confirmKctMigration());
    if (done) {
      setResult(done);
      report.reload();
      onMigrated?.();
    }
  }

  if (report.loading && !state) {
    return (
      <Panel title="独立版迁移">
        <SkeletonRows rows={2} cols={1} />
      </Panel>
    );
  }
  if (report.error && !state) {
    return (
      <Panel title="独立版迁移">
        <ErrorNotice
          message={errorMessage(report.error)}
          recovery="增强插件在完成迁移前不能启用。稍后重试；仍然失败时确认核心服务正在运行。"
        />
      </Panel>
    );
  }
  if (!needsConfirmation && !migrated) {
    return null;
  }

  return (
    <Panel title={migrated ? "独立版迁移" : "先停用独立版 KCT"}>
      {migrated ? (
        <>
          <p className="panel-note">
            独立版的后台加载器已停用{state?.migratedAt ? `（${fmtDateTime(state.migratedAt)}）` : ""}。继续使用这里的增强插件即可，思考内容只会被注入一次。
          </p>
          {state?.dataDirPresent ? (
            <p className="panel-note">
              独立版的历史数据仍保留在 <code>{state.dataDir}</code>，增强插件不会读取它。确认不再需要后可以自行删除。
            </p>
          ) : null}
        </>
      ) : (
        <>
          <p>
            检测到独立版 KCT 仍在后台运行。它和这个增强插件都会改写思考内容，两套同时工作会让同一段内容被注入两次。确认迁移后，核心会停用它的后台加载器，并把目标语言和翻译服务带过来。
          </p>
          <dl className="rail-facts">
            <div>
              <dt>后台加载器</dt>
              <dd className="num">{state?.plistPresent ? "已安装，下次登录会启动" : "未安装启动项"}</dd>
            </div>
            <div>
              <dt>数据目录</dt>
              <dd className="num">{state?.dataDirPresent ? state.dataDir : "不存在"}</dd>
            </div>
            <div>
              <dt>将迁移的设置</dt>
              <dd>
                {Object.keys(state?.settings ?? {}).length === 0
                  ? "没有可迁移的公共设置"
                  : Object.entries(state?.settings ?? {})
                      .map(([key, value]) => `${key === "targetLanguage" ? "目标语言" : "翻译服务"} ${value}`)
                      .join("、")}
              </dd>
            </div>
          </dl>
          <InfoNote tone="warn">
            <p>迁移只停用 com.user.kimi-cot-translate.runtime 这一个启动项，不会删除独立版的文件和数据。密钥不会迁移：独立版的密钥在它自己的界面里，本增强插件需要重新保存。</p>
          </InfoNote>
          <div className="inline-actions">
            <ActionButton variant="primary" icon="check" onClick={() => void confirm()} busy={action.busy}>
              确认迁移并停用独立版
            </ActionButton>
            <ActionButton variant="quiet" onClick={report.reload} busy={report.loading}>
              重新检测
            </ActionButton>
          </div>
          <p className="panel-note">不确认也可以：增强插件保持停用，独立版继续工作，两边不会同时注入。</p>
        </>
      )}
      {action.error ? (
        <ErrorNotice
          message={errorMessage(action.error)}
          recovery="独立版没有被改动。排除原因后可以再次确认；这个操作可以安全重复。"
          onDismiss={action.clearError}
        />
      ) : null}
      {result?.message ? <p className="panel-note">{result.message}</p> : null}
      {state?.detail ? <p className="panel-note">{state.detail}</p> : null}
    </Panel>
  );
}

/**
 * The handover from the standalone kimi-code-monitor desktop patch. The patch
 * injects its own sidebar panel into Kimi Code's `desktop-dist`; enabling the
 * bundled plugin while it is still in place would mount two panels. The restore
 * is a core route because the plugin is blocked while the patch exists.
 */
function MonitorMigrationPanel({ show, onMigrated }: { show: boolean; onMigrated?: () => void }) {
  const report = useResource("plugins/monitor/migration", () => api.plugins.monitorMigration(), { enabled: show });
  const action = useAction<MonitorMigrationResult>();
  const [result, setResult] = useState<MonitorMigrationResult | null>(null);

  if (!show) return null;
  const state = result?.report ?? report.data ?? null;
  const needsConfirmation = Boolean(state?.needsConfirmation);
  const migrated = Boolean(state?.migrated);

  async function confirm() {
    const done = await action.run(() => api.plugins.confirmMonitorMigration());
    if (done) {
      setResult(done);
      report.reload();
      onMigrated?.();
    }
  }

  if (report.loading && !state) {
    return (
      <Panel title="独立版 Monitor 迁移">
        <SkeletonRows rows={2} cols={1} />
      </Panel>
    );
  }
  if (report.error && !state) {
    return (
      <Panel title="独立版 Monitor 迁移">
        <ErrorNotice
          message={errorMessage(report.error)}
          recovery="增强插件在确认迁移前不能启用。稍后重试；仍然失败时确认核心服务正在运行。"
        />
      </Panel>
    );
  }
  if (!needsConfirmation && !migrated) {
    return null;
  }

  return (
    <Panel title={migrated ? "独立版 Monitor 迁移" : "先卸载独立版 Monitor 补丁"}>
      {migrated ? (
        <>
          <p className="panel-note">
            {result?.message || "独立版 Kimi Code Monitor 的桌面补丁已卸载"}
            {state?.migratedAt ? `（${fmtDateTime(state.migratedAt)}）` : ""}。原始的 <code>index.html</code> 已经恢复，
            <code>kcm/</code> 目录也已删除，现在可以启用这个增强插件。
          </p>
          <InfoNote tone="warn">
            <p>
              如果旧窗口里已经加载过独立版侧栏面板，请按 <kbd>Cmd+R</kbd>（Windows 为 <kbd>Ctrl+R</kbd>）重新载入 Kimi Code，
              再启用「Monitor」，避免侧栏同时出现两个面板。
            </p>
          </InfoNote>
        </>
      ) : (
        <>
          <p>
            检测到独立版 <code>kimi-code-monitor</code> 的桌面补丁仍在 Kimi Code 的 <code>desktop-dist</code> 中：
            <code>kcm/</code> 仍在，<code>index.html</code> 仍会加载它的 <code>kcm/loader.js</code>。这个增强插件和独立版都会向侧栏
            注入 Monitor 面板，两套同时存在会看到两个重叠的面板。确认后会还原原始 <code>index.html</code> 并删除 <code>kcm/</code>。
          </p>
          <dl className="rail-facts">
            <div>
              <dt>桌面目录</dt>
              <dd className="num">{state?.distDir || "未找到"}</dd>
            </div>
            <div>
              <dt>独立版载荷</dt>
              <dd className="num">{state?.payloadPresent ? state.payloadDir : "不在了"}</dd>
            </div>
            <div>
              <dt>原始页面备份</dt>
              <dd className="num">{state?.backupPresent ? state.backupPath : "没有 index.html.bak-kcm，将只清理补丁标记"}</dd>
            </div>
          </dl>
          <InfoNote tone="warn">
            <p>卸载只还原 Kimi Code 的桌面页面并删除补丁载荷，不会删除独立版自己的数据目录，也不会改动这个增强插件的设置。</p>
          </InfoNote>
          <div className="inline-actions">
            <ActionButton variant="primary" icon="check" onClick={() => void confirm()} busy={action.busy}>
              停用并卸载独立版 Monitor 补丁
            </ActionButton>
            <ActionButton variant="quiet" onClick={report.reload} busy={report.loading}>
              重新检测
            </ActionButton>
          </div>
          <p className="panel-note">不确认也可以：增强插件保持停用，独立版继续工作，两边不会同时显示。</p>
        </>
      )}
      {action.error ? (
        <ErrorNotice
          message={errorMessage(action.error)}
          recovery="独立版补丁没有被改动。排除原因后可以再次确认；这个操作可以安全重复。"
          onDismiss={action.clearError}
        />
      ) : null}
      {!migrated && result?.message ? <p className="panel-note">{result.message}</p> : null}
      {state?.detail ? <p className="panel-note">{state.detail}</p> : null}
    </Panel>
  );
}

const SCHEDULER_PLUGIN_ID = "kimicode-plus.scheduler";

interface SchedulerAttachPromptState {
  pluginName: string;
  platform: string;
  autoAttachKind: string;
  available: boolean;
  unavailableReason: string;
  dailyInstance: boolean;
}

export interface PluginSettingsProps {
  /** Rendered inside the settings dialog, where the page header is the dialog's. */
  embedded?: boolean;
  /** Native host bridge for local package picking and scheduler auto-attach. */
  host?: NativeHost | null;
  /** Notifies the host settings surface when entering or leaving a single-plugin detail subpage. */
  onDetailChange?: (active: boolean) => void;
}

export function PluginSettings({ embedded = false, host = null, onDetailChange }: PluginSettingsProps) {
  const plugins = useResource("plugins/list", () => api.plugins.list());
  const updates = useResource("plugins/updates", () => api.plugins.updates());
  const [selected, setSelected] = useState<string | null>(null);
  const returnToRef = useRef<string | null>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const rowRefs = useRef(new Map<string, HTMLButtonElement>());
  const [flash, setFlash] = useState<string | null>(null);
  const [installOpen, setInstallOpen] = useState(false);
  const [installPath, setInstallPath] = useState("");
  const [installError, setInstallError] = useState<string | null>(null);
  const [preview, setPreview] = useState<PluginPreview | null>(null);
  const [granted, setGranted] = useState<string[]>([]);
  const [reconfirmGrants, setReconfirmGrants] = useState(false);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [pickerBusy, setPickerBusy] = useState<"directory" | "zip" | null>(null);
  const [confirmPrivileged, setConfirmPrivileged] = useState(false);
  const [confirmForce, setConfirmForce] = useState(false);
  const [confirmUninstall, setConfirmUninstall] = useState(false);
  const [autoAttachPrompt, setAutoAttachPrompt] = useState<SchedulerAttachPromptState | null>(null);
  const [autoAttachBusy, setAutoAttachBusy] = useState(false);
  const [autoAttachConsent, setAutoAttachConsent] = useState(false);
  const [autoAttachError, setAutoAttachError] = useState<string | null>(null);
  const action = useAction<PluginRecord | { id: string; uninstalled: boolean }>();
  const [syncing, setSyncing] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [quickBusy, setQuickBusy] = useState<string | null>(null);
  const [quickError, setQuickError] = useState<string | null>(null);
  const [updateSnapshot, setUpdateSnapshot] = useState<PluginUpdateStatus | null>(null);
  const [checkingUpdates, setCheckingUpdates] = useState(false);
  const [updateCheckError, setUpdateCheckError] = useState<string | null>(null);
  const [updatingPluginId, setUpdatingPluginId] = useState<string | null>(null);
  const [confirmUpdatePrivileged, setConfirmUpdatePrivileged] = useState(false);

  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(null), 3600);
    return () => clearTimeout(timer);
  }, [flash]);

  const items = useMemo(
    () => (plugins.data?.items ?? []).filter(item => item.id !== PLANNER_PLUGIN_ID),
    [plugins.data?.items],
  );
  const current = useMemo(() => items.find(item => item.id === selected) ?? null, [items, selected]);
  const detailActive = Boolean(current);
  const previewPrivileged = preview?.privilegedCapabilities ?? [];
  const needsPrivilegeConfirmation = granted.some(name => previewPrivileged.includes(name));

  useEffect(() => {
    onDetailChange?.(detailActive);
  }, [detailActive, onDetailChange]);

  useEffect(() => {
    if (detailActive) {
      let currentNode: Node | null = backRef.current;
      while (currentNode) {
        if (currentNode instanceof HTMLElement) {
          if (currentNode.scrollTop > 0) {
            currentNode.scrollTop = 0;
          }
          if (
            currentNode.classList.contains("settings-dialog") ||
            currentNode.classList.contains("ui-dialog") ||
            currentNode.getAttribute("role") === "dialog"
          ) break;
        }
        if (currentNode.parentElement) {
          currentNode = currentNode.parentElement;
        } else {
          const root = currentNode.getRootNode?.();
          currentNode = root && "host" in root ? (root as ShadowRoot).host : null;
        }
      }
      backRef.current?.focus({ preventScroll: true });
    } else if (returnToRef.current) {
      const target = rowRefs.current.get(returnToRef.current);
      target?.scrollIntoView?.({ block: "nearest" });
      target?.focus();
      returnToRef.current = null;
    }
  }, [detailActive]);

  const effectiveUpdates = updateSnapshot ?? updates.data ?? null;
  const updateItems = effectiveUpdates?.items ?? {};

  function openDetail(id: string) {
    returnToRef.current = id;
    setConfirmForce(false);
    setConfirmUninstall(false);
    setConfirmUpdatePrivileged(false);
    setSelected(id);
  }

  function closeDetail() {
    if (selected) returnToRef.current = selected;
    setConfirmForce(false);
    setConfirmUninstall(false);
    setConfirmUpdatePrivileged(false);
    setSelected(null);
  }

  async function checkUpdates() {
    if (checkingUpdates || updatingPluginId !== null) return;
    setCheckingUpdates(true);
    setUpdateCheckError(null);
    setQuickError(null);
    try {
      const status = await api.plugins.checkUpdates();
      setUpdateSnapshot(status);
      const visibleAvailable = Object.values(status.items ?? {}).filter(
        item => item.updateAvailable && item.pluginId !== PLANNER_PLUGIN_ID,
      ).length;
      const plannerAvailable = Boolean(status.items?.[PLANNER_PLUGIN_ID]?.updateAvailable);
      if (visibleAvailable > 0) {
        setFlash(`发现 ${visibleAvailable} 个官方增强插件可在线更新。`);
      } else if (plannerAvailable) {
        setFlash("「规划」插件有可用更新，可在「实验室」标签页一键更新。");
      } else {
        setFlash("当前官方增强插件均已是最新版本。");
      }
    } catch (error) {
      setUpdateCheckError(errorMessage(error) || "无法检查插件更新，请稍后重试。");
    } finally {
      setCheckingUpdates(false);
    }
  }

  async function applyPluginUpdate(plugin: PluginRecord, update: PluginUpdateItem, confirmConsent = false) {
    if (updatingPluginId !== null || checkingUpdates || syncing || action.busy) return;
    const hasAddedPriv = (update.addedPrivileged?.length ?? 0) > 0;
    if (update.needsConsent && !confirmConsent) {
      openDetail(plugin.id);
      return;
    }
    if (hasAddedPriv && !confirmUpdatePrivileged) return;
    setUpdatingPluginId(plugin.id);
    setQuickError(null);
    setUpdateCheckError(null);
    action.clearError();
    try {
      const updated = await api.plugins.update(plugin.id, {
        expectedVersion: update.latestVersion,
        confirmCapabilities: update.needsConsent ? true : undefined,
        confirmPrivileged: hasAddedPriv ? true : undefined,
      });
      // The files are already replaced, so the sync failure is a warning, not a
      // rollback: reporting both "updated" and "failed" would contradict itself.
      const syncMessage = await syncPluginHost();
      setConfirmUpdatePrivileged(false);
      if (syncMessage) {
        const message = `已热更新到 v${updated.version}，但界面同步失败：${syncMessage}`;
        if (selected === plugin.id) setUpdateCheckError(message);
        else setQuickError(message);
      } else {
        setFlash(`已将「${updated.name || updated.id}」热更新到 v${updated.version}，无需重启 Kimi Code。`);
      }
      plugins.reload();
      const refreshed = await api.plugins.updates().catch(() => null);
      if (refreshed) setUpdateSnapshot(refreshed);
      else updates.reload();
      if (selected === plugin.id) activity.reload();
    } catch (error) {
      const message = errorMessage(error) || "插件更新失败，已保持原版本。";
      if (selected === plugin.id) setUpdateCheckError(message);
      else setQuickError(message);
    } finally {
      setUpdatingPluginId(null);
    }
  }
  const activity = useResource<PluginActivity[]>(
    `plugins/activity/${selected ?? ""}`,
    async () => (selected ? (await api.plugins.activity(selected)).items : []),
    { enabled: Boolean(selected) },
  );

  async function syncHost() {
    setSyncing(true);
    setSyncError(null);
    try {
      setSyncError(await syncPluginHost());
    } finally {
      setSyncing(false);
    }
  }

  async function maybePromptSchedulerAutoAttach(record: PluginRecord) {
    const isScheduler = record.id === SCHEDULER_PLUGIN_ID
      || (record.capabilities ?? []).includes("schedule:manage");
    if (!isScheduler || !host) return;
    try {
      const status = await host.request<{
        host?: { dailyInstance?: boolean };
        agent?: { enabled?: boolean };
        platform?: string;
        autoAttachKind?: string;
        available?: boolean;
        unavailableReason?: string;
      }>("attach.status");
      if (status?.agent?.enabled) {
        setAutoAttachPrompt(null);
        return;
      }
      setAutoAttachConsent(false);
      setAutoAttachError(null);
      setAutoAttachPrompt({
        pluginName: record.name || record.id,
        platform: status?.platform ?? "",
        autoAttachKind: status?.autoAttachKind ?? "",
        available: status?.available !== false,
        unavailableReason: status?.unavailableReason ?? "",
        dailyInstance: status?.host?.dailyInstance === true,
      });
    } catch {
      // Do not block plugin activation if attach.status is temporarily unavailable.
    }
  }

  async function enableAutoAttachFromPrompt() {
    if (!host || !autoAttachPrompt) return;
    setAutoAttachBusy(true);
    setAutoAttachError(null);
    try {
      await host.request("attach.enable", { allowDailyInstance: autoAttachConsent });
      setAutoAttachPrompt(null);
      setFlash("已开启「随 Kimi Code 自动接入」。Kimi Code 重启后定时任务将自动恢复接入并按时运行。");
      if (typeof window !== "undefined") {
        window.dispatchEvent(new CustomEvent("ekd:attach-changed"));
      }
    } catch (error) {
      setAutoAttachError(errorMessage(error) || "开启自动接入失败，请稍后在「状态与诊断」中重试。");
    } finally {
      setAutoAttachBusy(false);
    }
  }

  async function afterStateChange(record: PluginRecord) {
    await syncHost();
    setFlash(`「${record.name || record.id}」现在${PLAIN_STATE[record.state] ?? stateLabel(record.state)}。`);
    plugins.reload();
    activity.reload();
  }

  async function run(id: string, kind: "enable" | "disable" | "uninstall", options?: { deleteData?: boolean }) {
    if (kind === "uninstall") {
      const done = await action.run(() => api.plugins.uninstall(id, options));
      if (done) {
        await syncHost();
        setConfirmUninstall(false);
        setSelected(null);
        setFlash(
          options?.deleteData
            ? "增强插件及其定时任务数据已卸载并删除。卸载后的预装增强插件不会自动恢复。"
            : "增强插件已卸载，相关任务数据已保留。卸载后的预装增强插件不会自动恢复。",
        );
        plugins.reload();
      }
      return;
    }
    const result = await action.run(() => (kind === "enable" ? api.plugins.enable(id) : api.plugins.disable(id)));
    if (result && "state" in result) {
      await afterStateChange(result);
      if (kind === "enable") {
        await maybePromptSchedulerAutoAttach(result);
      } else if (id === SCHEDULER_PLUGIN_ID) {
        setAutoAttachPrompt(null);
      }
    }
  }

  /* The list row cannot host a migration panel, so a gated enable lands the user
     on the detail page that owns the confirmation. */
  async function quickToggle(plugin: PluginRecord, next: boolean) {
    if (quickBusy !== null) return;
    setQuickError(null);
    setQuickBusy(plugin.id);
    try {
      const record = next ? await api.plugins.enable(plugin.id) : await api.plugins.disable(plugin.id);
      await afterStateChange(record);
      if (next && record.state === "BLOCKED") {
        setFlash(`「${record.name || record.id}」被门禁拦截，已打开详情页查看原因。`);
        openDetail(plugin.id);
        return;
      }
      if (next) {
        await maybePromptSchedulerAutoAttach(record);
      } else if (plugin.id === SCHEDULER_PLUGIN_ID) {
        setAutoAttachPrompt(null);
      }
    } catch (error) {
      if (isMigrationGate(error)) {
        setFlash(`「${plugin.name || plugin.id}」需要先完成独立版迁移，已打开详情页。`);
        openDetail(plugin.id);
        plugins.reload();
      } else {
        setQuickError(errorMessage(error));
      }
    } finally {
      setQuickBusy(null);
    }
  }

  function closeInstall() {
    setInstallOpen(false);
    setInstallPath("");
    setPreview(null);
    setGranted([]);
    setReconfirmGrants(false);
    setConfirmPrivileged(false);
    setInstallError(null);
    action.clearError();
  }

  async function pickInstallPath(kind: "directory" | "zip") {
    if (!host || pickerBusy || previewBusy) return;
    setPickerBusy(kind);
    setInstallError(null);
    try {
      let path: string | undefined;
      if (kind === "directory") {
        const picked = await host.request<{ path?: string }>("host.pickDirectory", { title: "选择增强插件文件夹" });
        path = picked?.path;
      } else {
        const files = await host.request<Array<{ path: string; name: string }>>("host.pickFiles", { title: "选择增强插件 .zip 包", filters: [{ name: "ZIP 插件包", extensions: ["zip"] }], single: true });
        path = (files?.find(file => /\.zip$/i.test(file.name || file.path)) ?? files?.[0])?.path;
      }
      if (path) setInstallPath(path);
    } catch (error) {
      setInstallError(errorMessage(error) || "无法打开文件选择器，请手动输入路径。");
    } finally {
      setPickerBusy(null);
    }
  }

  // Validation is a separate, side-effect-free step: the user sees the manifest
  // and the capability list before anything is written to disk, and the install
  // carries exactly the list they confirmed.
  async function inspect() {
    if (previewBusy || pickerBusy) return;
    action.clearError();
    setConfirmPrivileged(false);
    setInstallError(null);
    setPreview(null);
    setGranted([]);
    if (!installPath.trim()) {
      setInstallError("请填写增强插件文件夹或 zip 的完整路径。");
      return;
    }
    setPreviewBusy(true);
    try {
      const result = await api.plugins.preview(installPath.trim());
      setPreview(result);
      setGranted(reconfirmGrants ? [] : result.capabilities);
    } catch (error) {
      setInstallError(errorMessage(error) || "无法读取这个增强插件包。");
    } finally {
      setPreviewBusy(false);
    }
  }

  async function install() {
    if (!preview || action.busy || (needsPrivilegeConfirmation && !confirmPrivileged)) return;
    setInstallError(null);
    const installed = await action.run(async () => {
      try {
        return await api.installPlugin(installPath.trim(), preview.packageDigest, granted, needsPrivilegeConfirmation ? true : undefined);
      } catch (error) {
        if (error instanceof ApiError && error.code === "PLUGIN_PACKAGE_CHANGED") {
          setPreview(null);
          setGranted([]);
          setConfirmPrivileged(false);
          setReconfirmGrants(true);
        }
        throw error;
      }
    });
    if (installed && "state" in installed) {
      await syncHost();
      closeInstall();
      openDetail(installed.id);
      setFlash(`已安装「${installed.name}」。增强插件保持停用，可以从详情页启用。`);
      plugins.reload();
    }
  }

  async function forceEnable(id: string) {
    const result = await action.run(() => api.plugins.forceEnable(id));
    if (result && "state" in result) {
      await syncHost();
      setConfirmForce(false);
      setFlash(`「${result.name || result.id}」已强制启用。它未在当前宿主版本验证。`);
      plugins.reload();
      activity.reload();
      await maybePromptSchedulerAutoAttach(result);
    }
  }

  async function restore(id: string) {
    const result = await action.run(() => api.plugins.restore(id));
    if (result && "state" in result) {
      await syncHost();
      setFlash(`预装增强插件「${result.name || result.id}」已恢复，保持停用。`);
      plugins.reload();
      activity.reload();
    }
  }

  const safeMode = Boolean(plugins.data?.safeMode);
  const syncNotice = syncError ? (
    <div className="inline-actions">
      <ErrorNotice
        title="侧栏尚未同步"
        message={syncError}
        recovery="核心已保存这次变更。重试只刷新增强插件界面，不会重复写入。"
      />
      <ActionButton icon="refresh" onClick={() => void syncHost()} busy={syncing}>
        重新同步侧栏
      </ActionButton>
    </div>
  ) : null;

  const autoAttachDialog = autoAttachPrompt ? (
    <div
      className="plugin-confirm"
      role="dialog"
      aria-modal="true"
      aria-label="建议开启随 Kimi Code 自动接入"
      data-ekd-autoattach-prompt="true"
    >
      <p>
        <strong>建议开启「随 Kimi Code 自动接入」</strong>
      </p>
      <p>
        已启用「{autoAttachPrompt.pluginName}」，但当前未开启
        {autoAttachPrompt.autoAttachKind === "windows_launcher" || autoAttachPrompt.platform === "windows"
          ? " Windows 登录后自动接入"
          : " macOS 登录后自动接入"}
        。如果不开启，Kimi Code 重启后，定时任务要等您手动重新接入才会运行。
      </p>
      {autoAttachPrompt.dailyInstance ? (
        <label className="capability-choice">
          <input
            type="checkbox"
            checked={autoAttachConsent}
            onChange={event => setAutoAttachConsent(event.target.checked)}
          />
          <span>我知道开启后会接入当前日常 Kimi Code 客户端界面</span>
        </label>
      ) : null}
      {!autoAttachPrompt.available && autoAttachPrompt.unavailableReason ? (
        <p className="panel-note">{autoAttachPrompt.unavailableReason}</p>
      ) : null}
      {autoAttachError ? (
        <ErrorNotice message={autoAttachError} onDismiss={() => setAutoAttachError(null)} />
      ) : null}
      <div className="inline-actions">
        {autoAttachPrompt.available ? (
          <ActionButton
            variant="primary"
            icon="check"
            onClick={() => void enableAutoAttachFromPrompt()}
            busy={autoAttachBusy}
            disabled={autoAttachPrompt.dailyInstance && !autoAttachConsent}
          >
            立即开启自动接入
          </ActionButton>
        ) : null}
        <ActionButton variant="quiet" onClick={() => setAutoAttachPrompt(null)} disabled={autoAttachBusy}>
          暂不开启
        </ActionButton>
      </div>
    </div>
  ) : null;

  if (current && current.state === "UNINSTALLED") {
    return (
      <div className={embedded ? "ekds-plugins ekds-subpage" : "page"} data-ekd-plugin-detail={current.id}>
        <button type="button" className="ekds-back" ref={backRef} onClick={closeDetail}>
          <Icon name="arrowRight" size={14} />返回增强插件
        </button>
        <div className="plugins-detail-head">
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
        {syncNotice}
        {action.error ? (
          <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
        ) : null}
        <Panel title="预装副本">
          <p className="panel-note">
            这个增强插件随应用一起提供，已被卸载。恢复会重新释放内置副本；它不会自动回到列表，也不会自动启用。
          </p>
          <ActionButton icon="refresh" onClick={() => void restore(current.id)} busy={action.busy || syncing}>
            恢复预装增强插件
          </ActionButton>
        </Panel>
      </div>
    );
  }

  if (current) {
    const capabilities = current.capabilities ?? [];
    const privileged = current.privilegedCapabilities ?? [];
    const ungranted = (current.declaredCapabilities ?? []).filter(name => !capabilities.includes(name));
    const hasScheduler = capabilities.includes("schedule:manage")
      || (current.declaredCapabilities ?? []).includes("schedule:manage");
    const currentUpdate = updateItems[current.id];
    const hasUpdate = Boolean(currentUpdate?.updateAvailable);
    const addedCaps = currentUpdate?.addedCapabilities ?? [];
    const addedPrivCaps = currentUpdate?.addedPrivileged ?? [];
    const isUpdatingThis = updatingPluginId === current.id;
    return (
      <div className={embedded ? "ekds-plugins ekds-subpage" : "page"} data-ekd-plugin-detail={current.id}>
        <button type="button" className="ekds-back" ref={backRef} onClick={closeDetail}>
          <Icon name="arrowRight" size={14} />返回增强插件
        </button>

        <div className="plugins-detail-head">
          <div className="plugins-detail-identity">
            <span className="plugin-glyph" aria-hidden="true">
              <Icon name={current.state === "CORRUPT" || current.state === "FAULTED" ? "alert" : "plugins"} size={18} />
            </span>
            <div className="plugins-detail-title">
              <div className="plugins-detail-title-row">
                <h2>{current.name || current.id}</h2>
                <StateBadge state={current.state} tone={stateTone(current.state)} label={PLAIN_STATE[current.state] ?? stateLabel(current.state)} />
              </div>
              <span className="num">
                v{current.version} · {current.source === "BUNDLED" ? "预装" : "本地"} · {current.id}
              </span>
            </div>
          </div>
          <div className="plugins-detail-header-actions">
            {current.source === "BUNDLED" ? (
              <ActionButton
                icon="refresh"
                variant="quiet"
                onClick={() => void checkUpdates()}
                busy={checkingUpdates}
                disabled={updatingPluginId !== null || action.busy || syncing}
              >
                检查更新
              </ActionButton>
            ) : null}
            {current.enabled ? (
              <ActionButton icon="power" onClick={() => void run(current.id, "disable")} busy={action.busy || syncing} disabled={isUpdatingThis}>
                停用增强插件
              </ActionButton>
            ) : (
              <ActionButton
                variant="primary"
                icon="power"
                onClick={() => void run(current.id, "enable")}
                busy={action.busy || syncing}
                disabled={safeMode || isUpdatingThis}
              >
                启用增强插件
              </ActionButton>
            )}
            {!current.enabled && !current.compatible ? (
              <ActionButton icon="alert" onClick={() => setConfirmForce(true)} busy={action.busy || syncing} disabled={safeMode || isUpdatingThis}>
                强制启用
              </ActionButton>
            ) : null}
          </div>
        </div>
        {current.description ? <p className="plugins-detail-desc">{current.description}</p> : null}

        {flash ? (
          <div className="save-flash" role="status">
            <Icon name="check" size={16} />
            {flash}
          </div>
        ) : null}
        {autoAttachDialog}
        {syncNotice}
        {updateCheckError ? (
          <ErrorNotice message={updateCheckError} onDismiss={() => setUpdateCheckError(null)} />
        ) : null}
        {action.error ? (
          <ErrorNotice message={errorMessage(action.error)} recovery={errorRecovery(action.error)} onDismiss={action.clearError} />
        ) : null}

        {hasUpdate && currentUpdate ? (
          <section className="plugin-update-card" aria-label="官方插件更新" data-ekd-plugin-update={current.id}>
            <div className="plugin-update-head">
              <div>
                <strong>发现新版本 v{currentUpdate.latestVersion}</strong>
                <span className="plugin-update-meta num">
                  v{current.version} → v{currentUpdate.latestVersion}
                  {currentUpdate.archiveBytes ? ` · ${fmtBytes(currentUpdate.archiveBytes)}` : ""}
                  {currentUpdate.publishedAt ? ` · ${fmtDateTime(currentUpdate.publishedAt)}` : ""}
                </span>
              </div>
              {!currentUpdate.needsConsent && currentUpdate.canApply ? (
                <ActionButton
                  variant="primary"
                  icon="download"
                  onClick={() => void applyPluginUpdate(current, currentUpdate, false)}
                  busy={isUpdatingThis}
                  disabled={checkingUpdates || action.busy || syncing}
                >
                  {isUpdatingThis ? "正在更新…" : `更新到 v${currentUpdate.latestVersion}`}
                </ActionButton>
              ) : null}
            </div>
            {currentUpdate.releaseNotes ? (
              <p className="plugin-update-notes">{currentUpdate.releaseNotes}</p>
            ) : null}
            {!currentUpdate.canApply && currentUpdate.cannotApplyReason ? (
              <InfoNote tone="warn">
                <p>{currentUpdate.cannotApplyReason}</p>
              </InfoNote>
            ) : null}
            {currentUpdate.canApply && currentUpdate.needsConsent ? (
              <div className="plugin-update-consent">
                <InfoNote tone="warn">
                  <p>
                    <strong>v{currentUpdate.latestVersion} 申请了 {addedCaps.length} 项新增权限</strong>
                    ，请确认后再更新。更新会保留现有配置与私有存储，无需重启 Kimi Code。
                  </p>
                </InfoNote>
                <ul className="capability-list">
                  {addedCaps.map(name => {
                    const label = capabilityLabel(name);
                    return (
                      <li key={name}>
                        <Icon name="shield" size={14} />
                        <div>
                          <CapabilityTitle name={name} privileged={addedPrivCaps} />
                          <p>{label.detail}</p>
                        </div>
                      </li>
                    );
                  })}
                </ul>
                {addedPrivCaps.length > 0 ? (
                  <label className="capability-choice">
                    <input
                      type="checkbox"
                      checked={confirmUpdatePrivileged}
                      onChange={event => setConfirmUpdatePrivileged(event.target.checked)}
                    />
                    <span>我了解上述新增高权限能力的影响范围，确认授予并更新</span>
                  </label>
                ) : null}
                <div className="inline-actions">
                  <ActionButton
                    variant="primary"
                    icon="download"
                    onClick={() => void applyPluginUpdate(current, currentUpdate, true)}
                    busy={isUpdatingThis}
                    disabled={checkingUpdates || action.busy || syncing || (addedPrivCaps.length > 0 && !confirmUpdatePrivileged)}
                  >
                    {isUpdatingThis ? "正在更新…" : `确认新增权限并更新到 v${currentUpdate.latestVersion}`}
                  </ActionButton>
                </div>
              </div>
            ) : null}
          </section>
        ) : null}

        {current.error ? <PluginFaultNotice reason={current.error} /> : null}
        {current.platformLimitations?.length ? <WindowsPluginReopen host={host} /> : null}
        {!current.enabled && !current.compatible ? (
          <InfoNote tone="warn">
            <p>这个增强插件没有声明当前宿主版本，默认不会启用。强制启用后它会以未验证状态运行。</p>
          </InfoNote>
        ) : null}
        {confirmForce ? (
          <div className="plugin-confirm" role="dialog" aria-modal="true" aria-label="确认强制启用">
            <p>
              强制启用「{current.name || current.id}」意味着让一个没有在当前宿主版本验证过的增强插件运行。它可能读到你的会话内容。确认继续？
            </p>
            <div className="inline-actions">
              <ActionButton variant="danger" icon="check" onClick={() => void forceEnable(current.id)} busy={action.busy || syncing}>
                确认强制启用
              </ActionButton>
              <ActionButton variant="quiet" onClick={() => setConfirmForce(false)}>
                取消
              </ActionButton>
            </div>
          </div>
        ) : null}

        {current.id === KCT_PLUGIN_ID ? (
          <KctMigrationPanel show={!current.enabled || current.state === "BLOCKED"} onMigrated={() => plugins.reload()} />
        ) : null}
        {current.id === MONITOR_PLUGIN_ID ? (
          <MonitorMigrationPanel show={!current.enabled || current.state === "BLOCKED"} onMigrated={() => plugins.reload()} />
        ) : null}
        {current.hasSettings && current.enabled ? <PluginSettingsPanel plugin={current} /> : null}
        {current.hasSettings && !current.enabled ? (
          <div className="plugin-settings-disabled">
            <p className="panel-note">启用增强插件后即可在这里调整配置。</p>
            <ActionButton
              variant="primary"
              icon="power"
              onClick={() => void run(current.id, "enable")}
              busy={action.busy || syncing}
              disabled={safeMode}
            >
              立即启用
            </ActionButton>
          </div>
        ) : null}

        <PluginCredentials plugin={current} />

        <div className="plugin-details-stack">
          <details className="plugin-details-disclosure" open={ungranted.length > 0 || !current.hasSettings}>
            <summary>
              <span>权限与能力</span>
              <span className="num">{capabilities.length} 项{ungranted.length > 0 ? ` · ${ungranted.length} 项未授权` : ""}</span>
            </summary>
            <div className="plugin-details-body">
              {capabilities.length === 0 ? (
                <p className="panel-note">这个增强插件没有获得任何能力，只能显示自己的界面。</p>
              ) : (
                <ul className="capability-list">
                  {capabilities.map(name => {
                    const label = capabilityLabel(name);
                    return (
                      <li key={name}>
                        <Icon name="check" size={14} />
                        <div>
                          <CapabilityTitle name={name} privileged={privileged} />
                          <p>{label.detail}</p>
                          <CapabilityPlatformNotes name={name} limitations={current.platformLimitations} />
                        </div>
                      </li>
                    );
                  })}
                </ul>
              )}
              {ungranted.length > 0 ? (
                <InfoNote tone="warn">
                  <p>
                    当前不可用的能力
                    {ungranted.map((name, index) => (
                      <span key={name}>{index === 0 ? " · " : "、"}{capabilityLabel(name).title}{privileged.includes(name) ? "（高权限）" : ""}</span>
                    ))}。
                  </p>
                  {ungranted.map(name => <CapabilityPlatformNotes key={name} name={name} limitations={current.platformLimitations} />)}
                </InfoNote>
              ) : null}
            </div>
          </details>

          <details className="plugin-details-disclosure">
            <summary>
              <span>插件信息与开源许可</span>
              <span className="num">{current.license || "未声明"}</span>
            </summary>
            <div className="plugin-details-body">
              <dl className="rail-facts">
                <div><dt>作者</dt><dd>{current.author || "未声明"}</dd></div>
                {current.apiVersion ? <div><dt>插件 API</dt><dd className="num">v{current.apiVersion}</dd></div> : null}
                <div>
                  <dt>来源</dt>
                  <dd>{current.source === "BUNDLED" ? (current.official ? "内置预装，完整性已校验" : "预装") : "本地安装"}</dd>
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
              <PluginUpstreamInfo upstream={current} />
              <PluginLegalFiles key={`${current.id}/${current.installedAt ?? ""}/${current.version}/${current.updatedAt ?? ""}`} plugin={current} />
            </div>
          </details>

          <details className="plugin-details-disclosure">
            <summary>
              <span>活动记录</span>
              <span className="num">{activity.data?.length ?? 0} 条</span>
            </summary>
            <div className="plugin-details-body">
              {activity.loading ? (
                <SkeletonRows rows={3} cols={3} />
              ) : activity.error ? (
                <PageError error={activity.error} />
              ) : (activity.data ?? []).length === 0 ? (
                <p className="panel-note">还没有记录。启用、停用和增强插件发起的动作都会记录在这里。</p>
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
            </div>
          </details>
        </div>

        <div className="plugin-danger-bar">
          <p className="panel-note">
            卸载会删除增强插件文件和私有存储；预装增强插件卸载后仍可从列表中恢复。
          </p>
          <ActionButton
            variant="danger"
            icon="trash"
            onClick={() => setConfirmUninstall(true)}
            busy={action.busy || syncing}
            disabled={isUpdatingThis}
          >
            卸载
          </ActionButton>
        </div>
        {confirmUninstall ? (
          <div className="plugin-confirm" role="dialog" aria-modal="true" aria-label="确认卸载增强插件">
            <p>
              <strong>卸载「{current.name || current.id}」会删除增强插件文件及其私有存储，此操作不可撤销。</strong>
            </p>
            <p className="panel-note">
              预装增强插件卸载后不会自动恢复，需要时可在列表里手动恢复内置副本。
              {hasScheduler ? "该增强插件创建的定时任务也可选择一并删除。" : ""}
            </p>
            <div className="inline-actions">
              <ActionButton
                variant="danger"
                icon="trash"
                onClick={() => void run(current.id, "uninstall", { deleteData: true })}
                busy={action.busy || syncing}
                disabled={isUpdatingThis}
              >
                {hasScheduler ? "卸载并删除任务数据" : "确认卸载并删除私有存储"}
              </ActionButton>
              {hasScheduler ? (
                <ActionButton
                  onClick={() => void run(current.id, "uninstall", { deleteData: false })}
                  busy={action.busy || syncing}
                  disabled={isUpdatingThis}
                >
                  仅卸载，保留任务数据
                </ActionButton>
              ) : null}
              <ActionButton variant="quiet" onClick={() => setConfirmUninstall(false)} disabled={action.busy || syncing}>
                取消
              </ActionButton>
            </div>
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className={embedded ? "ekds-plugins" : "page"}>
      <div className="plugins-list-head">
        <div>
          {/* The settings dialog already prints this title above the section. */}
          {embedded ? null : <h2>增强插件</h2>}
          <p>
            {embedded
              ? "由本机安装与授权的 KimiCode+ 增强插件，与官方插件市场相互独立。点击插件行可查看详情与设置。"
              : "由本机安装与授权的 KimiCode+ 增强插件，与官方插件市场相互独立。只安装来源完全可信的第三方插件。"}
          </p>
        </div>
        <div className="plugins-list-actions">
          <ActionButton icon="download" onClick={() => void checkUpdates()} busy={checkingUpdates} disabled={updatingPluginId !== null}>
            检查更新
          </ActionButton>
          <ActionButton icon="refresh" onClick={plugins.reload} busy={plugins.loading}>
            刷新
          </ActionButton>
          <ActionButton variant="primary" icon="plus" aria-expanded={installOpen} onClick={() => installOpen ? closeInstall() : setInstallOpen(true)}
            disabled={safeMode || previewBusy || pickerBusy !== null || action.busy || syncing || updatingPluginId !== null}>
            安装本地增强插件
          </ActionButton>
        </div>
      </div>

      {flash ? (
        <div className="save-flash" role="status">
          <Icon name="check" size={16} />
          {flash}
        </div>
      ) : null}

      {updateCheckError ? <ErrorNotice message={updateCheckError} onDismiss={() => setUpdateCheckError(null)} /> : null}

      {autoAttachDialog}
      {syncNotice}
      {safeMode ? (
        <InfoNote tone="warn">
          <p>安全模式已开启。所有增强插件都已停用，安装与启用都会被拒绝。关闭安全模式后可以重新启用。</p>
        </InfoNote>
      ) : null}

      {installOpen ? (
        <Panel title={preview ? "确认能力并安装" : "从本地安装"}>
          {preview ? (
            <>
              {preview.description ? <p className="panel-note">{preview.description}</p> : null}
              <dl className="rail-facts">
                <div>
                  <dt>增强插件</dt>
                  <dd>{preview.name || preview.id}</dd>
                </div>
                <div>
                  <dt>版本</dt>
                  <dd className="num">{preview.version}</dd>
                </div>
                <div><dt>插件 API</dt><dd className="num">v{preview.apiVersion}</dd></div>
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
              <PluginUpstreamInfo upstream={preview} />
              {preview.platformLimitations?.length ? <WindowsPluginReopen host={host} /> : null}
              <InfoNote tone="warn">
                <p><strong>安装第三方增强插件前请确认信任边界</strong></p>
                <p>第三方增强插件运行在当前桌面界面中，与 KimiCode+ 共享同一运行环境，没有独立的系统沙箱：除当前界面会话内容外，还可能触及本机的增强能力与接口。因此只安装来源完全可信的插件。下面的能力清单用于声明与管理插件能调用哪些接口，不能替代你对插件代码的信任审查。</p>
              </InfoNote>
              {preview.installed ? (
                <InfoNote tone="warn">
                  <p>这个增强插件已经安装过。继续安装会替换它的文件，并按你下面勾选的能力重新授权；增强插件会保持停用，需要再手动启用。</p>
                </InfoNote>
              ) : null}
              {!preview.compatible ? (
                <InfoNote tone="warn">
                  <p>增强插件没有声明当前宿主版本。安装后需要"强制启用"才能运行，请先确认它可信。</p>
                </InfoNote>
              ) : null}
              {preview.capabilities.length === 0 ? (
                <p className="panel-note">这个增强插件没有申请任何能力，只能显示自己的界面。</p>
              ) : (
                <>
                  <p className="panel-note">勾选你愿意授予的能力。未勾选的能力增强插件无法使用。</p>
                  <ul className="capability-list">
                    {preview.capabilities.map(name => {
                      const label = capabilityLabel(name);
                      return (
                        <li key={name}>
                          <label className="capability-choice">
                            <input
                              type="checkbox"
                              checked={granted.includes(name)}
                              disabled={action.busy || syncing}
                              onChange={event => {
                                const checked = event.target.checked;
                                setGranted(current => checked ? [...current, name] : current.filter(item => item !== name));
                                if (previewPrivileged.includes(name)) setConfirmPrivileged(false);
                              }}
                            />
                            <span>
                              <CapabilityTitle name={name} privileged={previewPrivileged} />
                              <span className="capability-detail">{label.detail}</span>
                              <CapabilityPlatformNotes name={name} limitations={preview.platformLimitations} />
                            </span>
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                  {needsPrivilegeConfirmation ? (
                    <InfoNote tone="warn">
                      <p><strong>授予高权限前请确认来源</strong></p>
                      <p>高权限允许增强插件直接操作你勾选的宿主功能。请确认你信任插件来源和代码。</p>
                      <label className="capability-choice">
                        <input type="checkbox" checked={confirmPrivileged} disabled={action.busy || syncing}
                          onChange={event => setConfirmPrivileged(event.target.checked)} />
                        <span>我信任该插件来源，确认授予上述高权限能力</span>
                      </label>
                    </InfoNote>
                  ) : null}
                  {granted.length < preview.capabilities.length ? (
                    <InfoNote tone="warn">
                      <p>你只授予了 {granted.length} / {preview.capabilities.length} 项能力。</p>
                    </InfoNote>
                  ) : null}
                </>
              )}
            </>
          ) : (
            <>
              <Field label="增强插件文件夹或 zip 路径" htmlFor="plugin-install-path" hint="只从你选择的本地位置安装。安装后增强插件保持停用，确认能力后再启用。">
                <input
                  id="plugin-install-path"
                  className="input"
                  value={installPath}
                  disabled={previewBusy || pickerBusy !== null}
                  onChange={event => setInstallPath(event.target.value)}
                  onKeyDown={event => {
                    if (event.key === "Enter") void inspect();
                  }}
                  placeholder="/Users/你/增强插件目录 或 增强插件.zip"
                />
              </Field>
              {host ? (
                <div className="inline-actions">
                  <ActionButton onClick={() => void pickInstallPath("directory")} busy={pickerBusy === "directory"}
                    disabled={previewBusy || pickerBusy === "zip"}>选择插件文件夹</ActionButton>
                  <ActionButton onClick={() => void pickInstallPath("zip")} busy={pickerBusy === "zip"}
                    disabled={previewBusy || pickerBusy === "directory"}>选择 .zip 包</ActionButton>
                </div>
              ) : null}
            </>
          )}
          {installError ? <ErrorNotice message={installError} onDismiss={() => setInstallError(null)} /> : null}
          {action.error ? <ErrorNotice message={errorMessage(action.error)} onDismiss={action.clearError} /> : null}
          <div className="inline-actions">
            {preview ? (
              <>
                <ActionButton variant="primary" icon="check" onClick={() => void install()} busy={action.busy || syncing}
                  disabled={needsPrivilegeConfirmation && !confirmPrivileged}>
                  授予 {granted.length} 项能力并安装
                </ActionButton>
                <ActionButton variant="quiet" onClick={() => { setPreview(null); setGranted([]); setConfirmPrivileged(false); action.clearError(); }}
                  disabled={action.busy || syncing}>
                  重新选择
                </ActionButton>
              </>
            ) : (
              <ActionButton variant="primary" icon="check" onClick={() => void inspect()} busy={previewBusy} disabled={pickerBusy !== null}>
                检查增强插件包
              </ActionButton>
            )}
            <ActionButton variant="quiet" onClick={closeInstall} disabled={previewBusy || pickerBusy !== null || action.busy || syncing}>
              取消
            </ActionButton>
          </div>
        </Panel>
      ) : null}

      {quickError ? <ErrorNotice message={quickError} onDismiss={() => setQuickError(null)} /> : null}

      {plugins.loading ? (
        <SkeletonRows rows={4} cols={4} />
      ) : plugins.error ? (
        <PageError error={plugins.error} />
      ) : items.length === 0 ? (
        <EmptyState icon="plugins" title="还没有增强插件">
          安装一个本地增强插件，或检查预装增强插件是否被卸载。核心功能不依赖任何增强插件。
        </EmptyState>
      ) : (
        <ul className="plugin-list">
          {items.map(plugin => {
            const name = plugin.name || plugin.id;
            const summary = plugin.description?.trim();
            const update = updateItems[plugin.id];
            const hasUpdate = Boolean(update?.updateAvailable);
            const isUpdating = updatingPluginId === plugin.id;
            return (
              <li key={plugin.id} className={plugin.enabled ? "plugin" : "plugin muted"}>
                <button
                  type="button"
                  className="plugin-open"
                  ref={node => {
                    if (node) rowRefs.current.set(plugin.id, node);
                    else rowRefs.current.delete(plugin.id);
                  }}
                  onClick={() => openDetail(plugin.id)}
                >
                  <span className="plugin-glyph" aria-hidden="true">
                    <Icon name={plugin.state === "CORRUPT" || plugin.state === "FAULTED" ? "alert" : "plugins"} size={17} />
                  </span>
                  <span className="plugin-line">
                    <span className="plugin-meta">
                      <strong>{name}</strong>
                      <span className="num">
                        v{plugin.version} · {plugin.source === "BUNDLED" ? "预装" : "本地"}
                      </span>
                      {plugin.hasSettings ? <span className="plugin-tag">可配置</span> : null}
                      {hasUpdate && update ? (
                        <span className="plugin-tag is-update">可更新 v{update.latestVersion}</span>
                      ) : null}
                    </span>
                    {summary ? <span className="plugin-desc" title={plugin.description}>{summary}</span> : null}
                  </span>
                  <StateBadge state={plugin.state} tone={stateTone(plugin.state)} label={PLAIN_STATE[plugin.state] ?? stateLabel(plugin.state)} />
                  <span className="plugin-count num">
                    {plugin.state === "UNINSTALLED" ? "可恢复" : `${(plugin.capabilities ?? []).length} 项能力`}
                  </span>
                  <span className="plugin-chevron" aria-hidden="true">
                    <Icon name="arrowRight" size={13} />
                  </span>
                </button>
                <div className="plugin-row-actions">
                  {hasUpdate && update ? (
                    update.canApply && !update.needsConsent ? (
                      <ActionButton
                        icon="download"
                        onClick={() => void applyPluginUpdate(plugin, update, false)}
                        busy={isUpdating}
                        disabled={updatingPluginId !== null || checkingUpdates || quickBusy !== null || syncing}
                      >
                        {isUpdating ? "更新中…" : "更新"}
                      </ActionButton>
                    ) : (
                      <ActionButton
                        variant="quiet"
                        onClick={() => openDetail(plugin.id)}
                        disabled={updatingPluginId !== null}
                      >
                        查看更新
                      </ActionButton>
                    )
                  ) : null}
                  {canQuickToggle(plugin) ? (
                    <Toggle
                      hideLabel
                      label={plugin.enabled ? `停用「${name}」` : `启用「${name}」`}
                      checked={Boolean(plugin.enabled)}
                      disabled={quickBusy !== null || updatingPluginId !== null || (!plugin.enabled && safeMode)}
                      onChange={next => void quickToggle(plugin, next)}
                    />
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
