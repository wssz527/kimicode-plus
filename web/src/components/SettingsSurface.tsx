import { useCallback, useEffect, useState } from "react";
import { useAuth } from "../auth";
import { Icon } from "./Icon";
import { CacheSettings } from "./CacheSettings";
import { PluginSettings } from "./PluginSettings";
import { errorText } from "./workbench/pieces";
import { NativeSettings } from "./NativeSettings";
import { AccountSettingsSection, FallbackNotice, LabSettingsSection } from "./OfficialSettingsSections";
import { resolveSettingsFallbacks } from "../plugin-runtime/contracts";
import type { NativeHost } from "../native";
import "../styles/native-settings-surface.css";

export interface ContractProbe {
  id: string;
  group: "dom" | "api" | string;
  label: string;
  status: "available" | "disabled" | "untested" | string;
  reason?: string;
  detail?: string;
  checkedAt?: string;
}

export interface ContractReport {
  appVersion?: string;
  electron?: string;
  verified: boolean;
  versionNote?: string;
  updatedAt?: string;
  probes: ContractProbe[];
}

/**
 * The KimiCode+ settings tab renders three flat sections (缓存, 增强插件,
 * 状态与诊断). Features that belong to official tabs (账户, 智能体与会话,
 * 实验室) mount directly into those official tabs and only fall back here when
 * their corresponding DOM probe is disabled.
 */
export function SettingsWindow({
  host,
  initialContracts = null,
}: {
  host: NativeHost;
  initialContracts?: ContractReport | null;
}) {
  const [contracts, setContracts] = useState<ContractReport | null>(initialContracts);
  const [pluginDetailOpen, setPluginDetailOpen] = useState(false);

  const loadContracts = useCallback(async () => {
    try {
      const next = await host.request<ContractReport>("contracts.status");
      if (next && typeof next === "object") setContracts(next);
    } catch {
      // DiagnosticsSection reports any transport errors directly.
    }
  }, [host]);

  useEffect(() => {
    void loadContracts();
  }, [loadContracts]);

  const fallbacks = resolveSettingsFallbacks(contracts?.probes);

  return (
    <div className="ekds ekds-flat" data-ekd-settings-root="flat">
      <section className="ekds-sec" data-ekd-section="cache" hidden={pluginDetailOpen}>
        <div className="ekds-sec-head">
          <h2 className="ekds-sec-title">缓存</h2>
        </div>
        <p className="ekds-sec-desc">
          查看本机缓存命中、会话空闲时长和实际用量，配置保活、空闲压缩与额度恢复。
        </p>
        <CacheSettings host={host} />
      </section>

      <section className="ekds-sec" data-ekd-section="plugins">
        {pluginDetailOpen ? null : (
          <>
            <div className="ekds-sec-head">
              <h2 className="ekds-sec-title">增强插件</h2>
            </div>
            <p className="ekds-sec-desc">
              管理 KimiCode+ 的本地增强插件、授权能力与运行状态，与官方插件市场相互独立。
            </p>
          </>
        )}
        <PluginSettings embedded host={host} onDetailChange={setPluginDetailOpen} />
      </section>

      {!pluginDetailOpen && fallbacks.account ? (
        <section className="ekds-sec" data-ekd-fallback="account">
          <AccountSettingsSection
            host={host}
            fallbackReason={fallbacks.account.reason || "官方「账户」设置页结构已变"}
          />
        </section>
      ) : null}

      {!pluginDetailOpen && fallbacks.agent ? (
        <section className="ekds-sec" data-ekd-fallback="agent">
          <div className="ekds-sec-head">
            <h2 className="ekds-sec-title">智能体与会话</h2>
          </div>
          <FallbackNotice
            label="智能体与会话"
            reason={fallbacks.agent.reason || "官方「智能体与会话」设置页结构已变"}
          />
          <NativeSettings embedded mode="all" />
        </section>
      ) : null}

      {!pluginDetailOpen && fallbacks.lab ? (
        <section className="ekds-sec" data-ekd-fallback="lab">
          <LabSettingsSection
            host={host}
            fallbackReason={fallbacks.lab.reason || "官方「实验室」设置页结构已变"}
          />
        </section>
      ) : null}

      <section className="ekds-sec" data-ekd-section="diagnostics" hidden={pluginDetailOpen}>
        <div className="ekds-sec-head">
          <h2 className="ekds-sec-title">状态与诊断</h2>
        </div>
        <p className="ekds-sec-desc">
          查看增强核心与官方客户端的接入状态、各项契约检测结果、接入日志与开源许可。
        </p>
        <DiagnosticsSection host={host} contracts={contracts} onContractsChange={setContracts} />
      </section>
    </div>
  );
}

type AttachStatus = {
  platform?: string;
  autoAttachKind?: string;
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
    debugPort?: number;
    debugPortWarning?: string;
  };
  agent: { enabled: boolean; path?: string; error?: string; shortcutCount?: number; backupDir?: string };
  last?: {
    status?: string;
    error?: string;
    consentRequired?: boolean;
    hostPid?: number;
    appVersion?: string;
    electron?: string;
    verified?: boolean;
    versionNote?: string;
    daily?: boolean;
    platform?: string;
    debugPort?: number;
    debugPortWarning?: string;
  };
  available: boolean;
  unavailableReason?: string;
};

const CONTRACT_STATUS_TEXT: Record<string, string> = {
  available: "可用",
  disabled: "已停用",
  untested: "未检测",
};

const CONTRACT_STATUS_CLASS: Record<string, string> = {
  available: "ready",
  disabled: "faulted",
  untested: "unknown",
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
function DiagnosticsSection({
  host,
  contracts,
  onContractsChange,
}: {
  host: NativeHost;
  contracts: ContractReport | null;
  onContractsChange: (next: ContractReport | null) => void;
}) {
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
      const [next, files, contractReport] = await Promise.all([
        host.request<AttachStatus>("attach.status"),
        host.request<Diagnostics>("diagnostics.get"),
        host.request<ContractReport>("contracts.status").catch(() => null),
      ]);
      setStatus(next);
      setDiagnostics(files);
      if (contractReport) onContractsChange(contractReport);
      setError("");
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [host, onContractsChange]);

  useEffect(() => {
    void reload();
  }, [reload]);

  useEffect(() => {
    const onAttachChanged = (event: Event) => {
      const detail = (event as CustomEvent<{ source?: string }>).detail;
      if (detail?.source === "diagnostics") return;
      void reload();
    };
    window.addEventListener("ekd:attach-changed", onAttachChanged);
    return () => window.removeEventListener("ekd:attach-changed", onAttachChanged);
  }, [reload]);

  async function toggle(next: boolean) {
    setBusy(true);
    setError("");
    try {
      await host.request(next ? "attach.enable" : "attach.disable", next ? { allowDailyInstance: consent } : {});
      await reload();
      window.dispatchEvent(new CustomEvent("ekd:attach-changed", { detail: { source: "diagnostics" } }));
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
  const detectedVersion = contracts?.appVersion || status.host.appVersion || status.last?.appVersion || "未知";
  const versionNote = contracts?.versionNote || status.last?.versionNote || "";
  const isWindowsLauncher = status.autoAttachKind === "windows_launcher" || status.platform === "windows";
  const debugPortWarning = status.host.debugPortWarning || status.last?.debugPortWarning || "";
  const domProbes = (contracts?.probes ?? []).filter(probe => probe.group === "dom");
  const apiProbes = (contracts?.probes ?? []).filter(probe => probe.group === "api");
  return <div className="ekds-body">
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
        <div><dt>客户端版本</dt><dd>{detectedVersion}{versionNote ? `（${versionNote}）` : ""}</dd></div>
        <div><dt>调试开关</dt><dd>{FUSE_TEXT[status.host.fuse ?? ""] ?? "未知"}</dd></div>
        {isWindowsLauncher && status.host.debugPort ? (
          <div><dt>本机调试端口</dt><dd className="num">127.0.0.1:{status.host.debugPort}</dd></div>
        ) : (
          <div><dt>临时调试端口</dt><dd>{status.host.inspectorBusy ? "已被占用，本次不会接入" : "空闲"}</dd></div>
        )}
        {status.host.debugInstance ? <div><dt>启动参数</dt><dd>带调试参数的实例，不会自动接入</dd></div> : null}
      </dl> : null}
      {debugPortWarning ? <div className="ekds-row" data-ekd-debug-port-warning="true">
        <span className="ekds-row-label">
          <strong>调试端口安全告警</strong>
          <small>{debugPortWarning}</small>
        </span>
        <span className="ekds-state is-faulted">告警</span>
      </div> : null}
      {versionNote ? <div className="ekds-row" data-ekd-unverified-version="true">
        <span className="ekds-row-label">
          <strong>版本验证状态</strong>
          <small>{versionNote}</small>
        </span>
        <span className="ekds-state is-warn">未经人工验证</span>
      </div> : null}
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>{isWindowsLauncher ? "随 Kimi Code 启动自动接入" : "重启后自动恢复"}</strong>
          <small>
            {status.available
              ? (isWindowsLauncher
                ? (status.agent.enabled
                  ? `已通过固定路径启动器接管开始菜单与桌面快捷方式（已备份 ${status.agent.shortcutCount ?? 0} 个原快捷方式，关闭即可一键恢复）。`
                  : "开启后会先备份开始菜单和桌面上的 Kimi Code 快捷方式，再改为指向固定路径启动器；关闭可一键恢复原快捷方式。")
                : "由系统在客户端文件变化时触发，安装后默认只在当前用户生效。")
              : status.unavailableReason}
          </small>
        </span>
        <button
          type="button"
          className="ekds-btn"
          role="switch"
          aria-checked={status.agent.enabled}
          disabled={busy || !status.available}
          onClick={() => void toggle(!status.agent.enabled)}
        >
          {busy ? "处理中…" : status.agent.enabled ? (isWindowsLauncher ? "关闭并恢复" : "已开启") : "已关闭"}
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

    {contracts ? <section className="ekds-section" data-ekd-contracts="true">
      <div className="ekds-section-head">
        <h3>契约检测</h3>
        <button type="button" className="ekds-btn" onClick={() => void reload()}>重新检测</button>
      </div>
      <p className="ekds-note">按界面注入点与官方本地接口逐项检测，单项失效只停用对应功能，官方页探针不通过时对应内容会自动回落到本页。</p>
      {domProbes.length > 0 ? <div className="ekds-group">
        {domProbes.map(probe => <div className="ekds-row" key={probe.id} data-ekd-probe={probe.id}>
          <span className="ekds-row-label">
            <strong>{probe.label}</strong>
            <small>{probe.reason || probe.detail || (probe.status === "available" ? "检测通过" : "暂未检测")}</small>
          </span>
          <span className={`ekds-state is-${CONTRACT_STATUS_CLASS[probe.status] ?? "unknown"}`}>
            {CONTRACT_STATUS_TEXT[probe.status] ?? probe.status}
          </span>
        </div>)}
      </div> : null}
      {apiProbes.length > 0 ? <div className="ekds-group">
        {apiProbes.map(probe => <div className="ekds-row" key={probe.id} data-ekd-probe={probe.id}>
          <span className="ekds-row-label">
            <strong>{probe.label}</strong>
            <small>{probe.reason || probe.detail || (probe.status === "available" ? "接口与字段检测通过" : "暂未检测")}</small>
          </span>
          <span className={`ekds-state is-${CONTRACT_STATUS_CLASS[probe.status] ?? "unknown"}`}>
            {CONTRACT_STATUS_TEXT[probe.status] ?? probe.status}
          </span>
        </div>)}
      </div> : null}
    </section> : null}

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
  </div>;
}
