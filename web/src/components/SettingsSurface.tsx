import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth";
import { Icon } from "./Icon";
import { CacheSettings } from "./CacheSettings";
import { PluginSettings } from "./PluginSettings";
import { errorText } from "./workbench/pieces";
import { NativeSettings } from "./NativeSettings";
import { Badge, ErrorNotice, Spinner } from "./ui";
import { addonFeatureKeys, readAddonFeatures, writeAddonFeatures, type AddonFeatures, type NativeHost } from "../native";
import "../styles/native-settings-surface.css";

const SECTIONS = [
  { id: "cache", label: "缓存与额度", hint: "保活、自动压缩与恢复续跑" },
  { id: "native", label: "Kimi Code 配置", hint: "子代理、步数上限与权限规则" },
  { id: "plugins", label: "插件", hint: "Monitor 与故障隔离" },
  { id: "addons", label: "附加功能", hint: "规划与会话并排，实验性" },
  { id: "diagnostics", label: "状态与诊断", hint: "接入状态、日志与开源许可" },
] as const;
type SectionId = typeof SECTIONS[number]["id"];

const MANAGEMENT_PATHS = ["/models", "/credentials"] as const;
type ManagementPath = typeof MANAGEMENT_PATHS[number];

/**
 * The settings sheet is one list of real destinations. Models and credentials
 * open the management window: rendering those pages inside the official dialog
 * put a second navigation and a wide table into a 560px column.
 */
export function SettingsWindow({ host }: { host: NativeHost }) {
  const [openId, setOpenId] = useState<SectionId | null>(null);
  const [managementError, setManagementError] = useState<string | null>(null);
  const returnTo = useRef<SectionId | null>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const rows = useRef(new Map<SectionId, HTMLButtonElement>());

  useEffect(() => {
    if (openId) backRef.current?.focus();
    else if (returnTo.current) rows.current.get(returnTo.current)?.focus();
  }, [openId]);

  function openManagement(path: ManagementPath) {
    setManagementError(null);
    host.openManagement(path).catch((cause: unknown) => setManagementError(errorText(cause)));
  }

  if (!openId) {
    return <div className="ekds">
      {managementError ? <ErrorNotice title="管理窗口没有打开" message={managementError} recovery="确认宿主仍是支持的版本后重试。" /> : null}
      <div className="ekds-group" role="group" aria-label="KimiCode+ 设置">
        {SECTIONS.map(item => <button
          key={item.id}
          type="button"
          className="ekds-row ekds-row-button"
          ref={node => { if (node) rows.current.set(item.id, node); else rows.current.delete(item.id); }}
          onClick={() => setOpenId(item.id)}
        >
          <span className="ekds-row-label"><strong>{item.label}</strong><small>{item.hint}</small></span>
          <Icon name="arrowRight" size={15} className="ekds-chevron" />
        </button>)}
        <button
          type="button"
          className="ekds-row ekds-row-button"
          onClick={() => openManagement("/models")}
        >
          <span className="ekds-row-label"><strong>模型、供应商与凭据</strong><small>在独立管理窗口里编辑目录与加密密钥</small></span>
          <span className="ekds-row-tail">
            <span className="ekds-row-aside">独立窗口</span>
            <Icon name="arrowUpRight" size={15} className="ekds-chevron" />
          </span>
        </button>
      </div>
    </div>;
  }

  const section = SECTIONS.find(item => item.id === openId) ?? SECTIONS[0];
  return <div className="ekds">
    <div className="ekds-subpage">
      <button
        type="button"
        className="ekds-back"
        ref={backRef}
        onClick={() => { returnTo.current = openId; setOpenId(null); }}
      >
        <Icon name="arrowRight" size={14} />返回
      </button>
      <header className="ekds-subpage-head">
        <h2>{section.label}</h2>
        <p>{section.hint}</p>
      </header>
      <div className="ekds-body">
        {openId === "cache" ? <CacheSettings host={host} /> : null}
        {openId === "native" ? <NativeSettings embedded /> : null}
        {openId === "plugins" ? <PluginsSection /> : null}
        {openId === "addons" ? <AddonsSection host={host} /> : null}
        {openId === "diagnostics" ? <DiagnosticsSection host={host} /> : null}
      </div>
    </div>
  </div>;
}

function PluginsSection() {
  return <>
    <p className="ekds-note">插件按清单申请的能力取用核心功能，停用后不影响核心与其他模块。</p>
    <PluginSettings embedded />
  </>;
}

const ADDON_COPY: Record<(typeof addonFeatureKeys)[number], { title: string; purpose: string }> = {
  planner: {
    title: "规划面板",
    purpose: "在右侧停靠或独立窗口里做多轮规划，再把结论交给 Code。入口出现在官方“侧边聊天”下方。",
  },
  multiSession: {
    title: "会话并排",
    purpose: "在同一个聊天区最多并排三个已有会话，用于对照和交叉引用。",
  },
};

/**
 * Addon switches save immediately and the panel state is taken from the host's
 * echoed value, so a failed write leaves the switch where the core really is.
 * Turning one off unloads its panel through the existing features event.
 */
function AddonsSection({ host }: { host: NativeHost }) {
  const [features, setFeatures] = useState<AddonFeatures | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    readAddonFeatures(host)
      .then(next => { if (alive) setFeatures(next); })
      .catch(cause => { if (alive) setError(errorText(cause)); });
    return () => { alive = false; };
  }, [host]);

  async function toggle(key: (typeof addonFeatureKeys)[number], value: boolean) {
    if (!features) return;
    setBusy(key);
    setError(null);
    try {
      setFeatures(await writeAddonFeatures(host, { ...features, [key]: value }));
    } catch (cause) {
      setError(errorText(cause));
      setFeatures(await readAddonFeatures(host).catch(() => features));
    } finally {
      setBusy(null);
    }
  }

  return <>
    <p className="ekds-note">这两项是实验功能，默认关闭。关闭后入口和已打开的面板都会移除，官方界面恢复原样。</p>
    {error ? <p className="ekds-note is-error">{error}</p> : null}
    <div className="ekds-group">
      {addonFeatureKeys.map(key => {
        const copy = ADDON_COPY[key];
        const on = features?.[key] === true;
        return <div className="ekds-row" key={key}>
          <span className="ekds-row-label">
            <strong>{copy.title}</strong>
            <small>{copy.purpose}</small>
          </span>
          <span className="ekds-row-controls">
            <Badge tone="warn">实验</Badge>
            <button
              type="button"
              className="ekds-btn"
              role="switch"
              aria-checked={on}
              aria-label={copy.title}
              disabled={!features || busy === key}
              onClick={() => void toggle(key, !on)}
            >
              {busy === key ? <Spinner size={12} /> : on ? "已开启" : "已关闭"}
            </button>
          </span>
        </div>;
      })}
    </div>
    {features === null && !error ? <p className="ekds-note">正在读取附加功能状态…</p> : null}
  </>;
}

type AttachStatus = {
  host: {
    running?: boolean;
    reason?: string;
    pid?: number;
    isolated?: boolean;
    dailyInstance?: boolean;
    debugInstance?: boolean;
    fuse?: string;
    inspectorBusy?: boolean;
    consentRequired?: boolean;
    appVersion?: string;
    electron?: string;
  };
  agent: { enabled: boolean; path?: string; error?: string };
  last?: { status?: string; error?: string; consentRequired?: boolean; hostPid?: number; appVersion?: string; daily?: boolean };
  available: boolean;
  unavailableReason?: string;
};

type DiagnosticsFile = {
  path: string;
  available: boolean;
  bytes?: number;
  tail?: string;
  head?: string;
  truncated?: boolean;
  note?: string;
};

type Diagnostics = { attachLog: DiagnosticsFile; notices: DiagnosticsFile };

const FUSE_TEXT: Record<string, string> = {
  ready: "允许临时调试端点",
  "signal-disabled": "已禁用调试信号，无法接入",
  "unknown-layout": "无法识别该版本的调试开关",
};

function byteText(bytes: number | undefined): string {
  if (bytes === undefined || !Number.isFinite(bytes)) return "";
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
}

/**
 * Connection state, host compatibility, the attach agent and the two real files
 * that explain a failed attach. The file excerpts come from the core with
 * secrets already stripped; the surface never claims a log it did not read.
 */
function DiagnosticsSection({ host }: { host: NativeHost }) {
  const { health } = useAuth();
  const [status, setStatus] = useState<AttachStatus | null>(null);
  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [consent, setConsent] = useState(false);
  const [openLog, setOpenLog] = useState(false);
  const [openNotices, setOpenNotices] = useState(false);

  const reload = useCallback(async () => {
    try {
      const [next, files] = await Promise.all([
        host.request<AttachStatus>("attach.status"),
        host.request<Diagnostics>("diagnostics.get"),
      ]);
      setStatus(next);
      setDiagnostics(files);
      setError("");
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [host]);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function toggle(next: boolean) {
    setBusy(true);
    setError("");
    try {
      await host.request(next ? "attach.enable" : "attach.disable", next ? { allowDailyInstance: consent } : {});
      await reload();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(false);
    }
  }

  if (!status) {
    return <p className="ekds-note">{error || "正在读取宿主接入状态…"}</p>;
  }
  const running = status.host.running === true;
  const daily = status.host.dailyInstance === true;
  const modules = health?.modules ?? [];
  const log = diagnostics?.attachLog;
  const notices = diagnostics?.notices;
  return <>
    <div className="ekds-group">
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>增强核心</strong>
          <small>{health?.status === "OK" ? `运行中 · 版本 ${health.version ?? "未知"}` : "未连接，下面的状态可能不是最新的"}</small>
        </span>
        <span className={`ekds-state is-${health?.status === "OK" ? "ready" : "unknown"}`}>{health?.status === "OK" ? "运行中" : "未连接"}</span>
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>Kimi Code</strong>
          <small>{running ? (daily ? "正在使用日常客户端" : "正在运行的隔离宿主") : status.host.reason || "未检测到正在运行的客户端"}</small>
        </span>
        <span className={`ekds-state is-${running ? "ready" : "unknown"}`}>{running ? "运行中" : "未运行"}</span>
      </div>
      {running ? <dl className="ekds-facts is-inset">
        <div><dt>进程号</dt><dd className="num">{status.host.pid ?? "—"}</dd></div>
        <div><dt>客户端版本</dt><dd>{status.host.appVersion ?? status.last?.appVersion ?? "未知"}</dd></div>
        <div><dt>调试开关</dt><dd>{FUSE_TEXT[status.host.fuse ?? ""] ?? "未知"}</dd></div>
        <div><dt>临时调试端口</dt><dd>{status.host.inspectorBusy ? "已被占用，本次不会接入" : "空闲"}</dd></div>
        {status.host.debugInstance ? <div><dt>启动参数</dt><dd>带调试参数的实例，不会自动接入</dd></div> : null}
      </dl> : null}
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>重启后自动恢复</strong>
          <small>{status.available ? "由系统在客户端文件变化时触发，安装后默认只在当前用户生效。" : status.unavailableReason}</small>
        </span>
        <button
          type="button"
          className="ekds-btn"
          role="switch"
          aria-checked={status.agent.enabled}
          disabled={busy || !status.available}
          onClick={() => void toggle(!status.agent.enabled)}
        >
          {busy ? "处理中…" : status.agent.enabled ? "已开启" : "已关闭"}
        </button>
      </div>
      {daily && !status.agent.enabled ? <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>接入日常客户端</strong>
          <small>当前检测到的是你正在使用的客户端。接入会改变它的界面，需要你明确同意。</small>
        </span>
        <label className="ekds-check">
          <input type="checkbox" checked={consent} onChange={event => setConsent(event.target.checked)} />
          我知道会改变当前界面
        </label>
      </div> : null}
      {status.agent.error ? <div className="ekds-row"><span className="ekds-row-label"><strong>后台接入状态</strong><small>{status.agent.error}</small></span></div> : null}
      {status.last ? <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>上次接入</strong>
          <small>{status.last.error || `已接入进程 ${status.last.hostPid ?? "—"}（客户端 ${status.last.appVersion ?? "未知版本"}）`}</small>
        </span>
        <span className={`ekds-state is-${status.last.status === "installed" ? "ready" : "faulted"}`}>{status.last.status === "installed" ? "成功" : "未接入"}</span>
      </div> : null}
    </div>
    {error ? <p className="ekds-note is-error">{error}</p> : null}

    <section className="ekds-section">
      <h3>模块</h3>
      {modules.length === 0 ? <p className="ekds-note">还没有读到模块状态。</p> : <div className="ekds-group">
        {modules.map(module => <div className="ekds-row" key={module.id}>
          <span className="ekds-row-label"><strong>{module.id}</strong><small>{module.message || "可用"}</small></span>
          <span className={`ekds-state is-${module.state.toLowerCase()}`}>{module.state}</span>
        </div>)}
      </div>}
    </section>

    <section className="ekds-section">
      <h3>日志与许可</h3>
      <div className="ekds-group">
        <button
          type="button"
          className="ekds-row ekds-row-button"
          aria-expanded={openLog}
          onClick={() => setOpenLog(value => !value)}
        >
          <span className="ekds-row-label">
            <strong>接入日志</strong>
            <small>{log?.available ? `${byteText(log.bytes)} · ${log.note ?? "显示末尾 8 KiB，已脱敏"}` : log?.note || "正在读取"}</small>
          </span>
          <Icon name={openLog ? "chevronUp" : "chevronDown"} size={15} className="ekds-chevron" />
        </button>
        {openLog && log ? <div className="ekds-file">
          <p className="ekds-file-path"><code>{log.path}</code></p>
          {log.available
            ? <pre className="ekds-log" tabIndex={0} aria-label="接入日志末尾">{log.tail}</pre>
            : <p className="ekds-note">{log.note}</p>}
        </div> : null}
        <button
          type="button"
          className="ekds-row ekds-row-button"
          aria-expanded={openNotices}
          onClick={() => setOpenNotices(value => !value)}
        >
          <span className="ekds-row-label">
            <strong>开源许可</strong>
            <small>{notices?.available ? `${byteText(notices.bytes)} · ${notices.note ?? "显示开头 2 KiB"}` : notices?.note || "正在读取"}</small>
          </span>
          <Icon name={openNotices ? "chevronUp" : "chevronDown"} size={15} className="ekds-chevron" />
        </button>
        {openNotices && notices ? <div className="ekds-file">
          <p className="ekds-file-path"><code>{notices.path}</code></p>
          {notices.available
            ? <pre className="ekds-log" tabIndex={0} aria-label="开源许可开头">{notices.head}</pre>
            : <p className="ekds-note">{notices.note}。打包后的发行包会把完整许可放在程序同目录。</p>}
        </div> : null}
      </div>
      <p className="ekds-note">日志由后台接入器写入，内容在核心侧脱敏后才会到达这里。系统日志里仍可能有未脱敏的原始行。</p>
    </section>
  </>;
}
