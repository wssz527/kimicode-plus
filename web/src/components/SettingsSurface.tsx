import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth";
import { Icon } from "./Icon";
import { CacheSettings } from "./CacheSettings";
import { WindowsAttachStatusPanel, type WindowsAttachReport } from "./WindowsAttachStatus";
import { errorText } from "./workbench/pieces";
import { NativeSettings } from "./NativeSettings";
import { Toggle } from "./ui";
import {
  AccountSettingsSection,
  FallbackNotice,
  LabSettingsSection,
  PluginsSettingsSection,
} from "./OfficialSettingsSections";
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

const FEATURE_ENTRIES = [
  { label: "缓存与用量", where: "「缓存与用量」标签页", tab: "cache", note: "命中率、会话空闲时长、保活与压缩策略。" },
  { label: "插件", where: "官方「插件 → KimiCode+」", tab: "plugins-ekd", note: "本地增强插件、配置与授权管理。" },
  { label: "实验室", where: "官方「实验室」标签页", tab: "lab", note: "会话并排与规划等实验性功能。" },
  { label: "账户", where: "官方「账户」标签页", tab: "account", note: "多账号切换、额度与登录状态。" },
  { label: "智能体与会话", where: "官方「智能体与会话」标签页", tab: "agent", note: "模型、权限与子智能体设置。" },
];

/**
 * The KimiCode+ settings tab is a single diagnostics surface plus a quiet
 * overview of where every KimiCode+ feature lives. Cache owns the dedicated
 * 「缓存与用量」tab; enhanced plugins mount into the official 「插件」tab. Features
 * that belong to official tabs (账户, 智能体与会话, 插件, 实验室) only fall back
 * here when their corresponding DOM probe is disabled.
 */
export function SettingsWindow({
  host,
  initialContracts = null,
}: {
  host: NativeHost;
  initialContracts?: ContractReport | null;
}) {
  const [contracts, setContracts] = useState<ContractReport | null>(initialContracts);
  const contractsPromise = useRef<Promise<ContractReport | null> | null>(null);

  const loadContracts = useCallback(async () => {
    // The diagnostics body and this summary both need the report on mount. One
    // shared in-flight request keeps them from hitting `contracts.status` twice.
    if (contractsPromise.current) return contractsPromise.current;
    const request = host.request<ContractReport>("contracts.status")
      .then((next) => {
        if (next && typeof next === "object") setContracts(next);
        return next ?? null;
      })
      .catch(() => null)
      .finally(() => { contractsPromise.current = null; });
    contractsPromise.current = request;
    return request;
  }, [host]);

  useEffect(() => {
    void loadContracts();
  }, [loadContracts]);

  // One report feeds both this summary and the diagnostics body; the panel's own
  // read and this initial read used to hit `contracts.status` twice on mount.
  const onContractsChange = useCallback((next: ContractReport | null) => setContracts(next), []);

  const fallbacks = resolveSettingsFallbacks(contracts?.probes);

  return (
    <div className="ekds ekds-flat" data-ekd-settings-root="flat">
      <section className="ekds-sec" data-ekd-section="diagnostics">
        <div className="ekds-sec-head">
          <h2 className="ekds-sec-title">状态与诊断</h2>
        </div>
        <p className="ekds-sec-desc">
          查看增强核心与官方客户端的接入状态、各项契约检测结果、接入日志与开源许可。
        </p>
        <DiagnosticsSection
          host={host}
          contracts={contracts}
          onContractsChange={onContractsChange}
          reloadContracts={loadContracts}
        />
      </section>

      <section className="ekds-sec" data-ekd-section="entries">
        <div className="ekds-sec-head">
          <h2 className="ekds-sec-title">功能入口</h2>
        </div>
        <p className="ekds-sec-desc">KimiCode+ 的各项功能都放在官方设置里对应的位置，不在这里重复提供开关。点击右侧位置可直接跳转。</p>
        <div className="ekds-group" data-ekd-entry-summary="true">
          {FEATURE_ENTRIES.map(entry => (
            <div className="ekds-row" key={entry.label}>
              <span className="ekds-row-label">
                <strong>{entry.label}</strong>
                <small>{entry.note}</small>
              </span>
              <button
                type="button"
                className="ekds-row-aside ekds-entry-link"
                title={`打开${entry.where}`}
                onClick={() => { void host.openSettings(entry.tab).catch(() => {}); }}
              >
                {entry.where}
                <span aria-hidden="true">›</span>
              </button>
            </div>
          ))}
        </div>
      </section>

      {fallbacks.account ? (
        <section className="ekds-sec" data-ekd-fallback="account">
          <AccountSettingsSection
            host={host}
            fallbackReason={fallbacks.account.reason || "官方「账户」设置页结构已变"}
          />
        </section>
      ) : null}

      {fallbacks.agent ? (
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

      {fallbacks.plugins ? (
        <section className="ekds-sec" data-ekd-fallback="plugins">
          <PluginsSettingsSection
            host={host}
            fallbackReason={fallbacks.plugins.reason || "官方「插件」设置页结构已变"}
          />
        </section>
      ) : null}

      {fallbacks.lab ? (
        <section className="ekds-sec" data-ekd-fallback="lab">
          <LabSettingsSection
            host={host}
            fallbackReason={fallbacks.lab.reason || "官方「实验室」设置页结构已变"}
          />
        </section>
      ) : null}
    </div>
  );
}

/**
 * The dedicated 「缓存与用量」 settings tab. Cache data is read-only history plus
 * opt-in policies, so it stands alone instead of sitting under 诊断.
 */
export function CacheSettingsSurface({ host }: { host: NativeHost }) {
  return (
    <div className="ekds ekds-flat" data-ekd-settings-root="cache">
      <section className="ekds-sec" data-ekd-section="cache">
        <div className="ekds-sec-head">
          <h2 className="ekds-sec-title">缓存与用量</h2>
        </div>
        <p className="ekds-sec-desc">
          查看本机缓存命中、会话空闲时长和实际用量，配置保活、空闲压缩与额度恢复。
        </p>
        <CacheSettings host={host} />
      </section>
    </div>
  );
}

type AttachStatus = WindowsAttachReport & {
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
  agent: { enabled: boolean; path?: string; error?: string; needsMigration?: boolean; corePath?: string; legacyCorePresent?: boolean };
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

export interface CoreUpdateStatus {
  state: "idle" | "checking" | "up_to_date" | "available" | "downloading" | "verifying" | "restarting" | "updated" | "rolled_back" | "error";
  currentVersion: string;
  latestVersion?: string;
  checkedAt?: string;
  publishedAt?: string;
  releaseUrl?: string;
  releaseNotes?: string;
  updateAvailable: boolean;
  canApply: boolean;
  cannotApplyReason?: string;
  autoCheck?: boolean;
  note?: string;
  error?: string;
  downloadedBytes: number;
  totalBytes: number;
  lastAttempt?: {
    state: "restarting" | "updated" | "rolled_back" | "error";
    targetVersion: string;
    previousVersion: string;
    completedAt?: string;
    error?: string;
  };
}

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

export function coreUpdateBusy(state?: CoreUpdateStatus["state"]): boolean {
  return state === "checking" || state === "downloading" || state === "verifying" || state === "restarting";
}

export function coreUpdateText(status: CoreUpdateStatus | null, reconnecting = false): string {
  if (reconnecting || status?.state === "restarting") return "正在重启并检查核心状态，请稍候…";
  switch (status?.state) {
    case "checking": return "正在检查更新…";
    case "downloading": {
      if (status.totalBytes > 0) {
        const percent = Math.min(100, Math.floor(100 * status.downloadedBytes / status.totalBytes));
        return `正在下载安装包 · ${percent}%`;
      }
      return status.downloadedBytes > 0 ? `正在下载安装包 · ${byteText(status.downloadedBytes)}` : "正在下载安装包…";
    }
    case "verifying": return "正在校验并准备安装…";
    case "available": return status.canApply ? "有新版本可更新" : "有新版本可手动下载";
    case "up_to_date": return status.note || "当前已是最新版本";
    case "updated": return `核心已更新至 ${status.currentVersion}`;
    case "rolled_back": return status.error || "更新失败，已回滚";
    case "error": return status.error || "更新操作失败，请重试";
    case "idle": return "尚未检查更新";
    default: return "正在读取版本状态…";
  }
}

export function checkCoreUpdate(host: Pick<NativeHost, "request">): Promise<CoreUpdateStatus> {
  return host.request<CoreUpdateStatus>("update.check");
}

export function setCoreAutoCheck(
  host: Pick<NativeHost, "request">,
  enabled: boolean,
): Promise<CoreUpdateStatus> {
  return host.request<CoreUpdateStatus>("update.autoCheck", { enabled });
}

export async function watchCoreUpdate(
  host: Pick<NativeHost, "request">,
  observe: (status: CoreUpdateStatus | null, reconnecting: boolean) => void,
  signal: AbortSignal,
  pause: () => Promise<void> = () => new Promise(resolve => setTimeout(resolve, 750)),
  now: () => number = Date.now,
): Promise<CoreUpdateStatus | null> {
  let restartDeadline: number | undefined;
  while (!signal.aborted) {
    const status = await host.request<CoreUpdateStatus>("update.status").catch(() => null);
    if (signal.aborted) break;
    const reconnecting = !status || status.state === "restarting";
    observe(status, reconnecting);
    if (status && !coreUpdateBusy(status.state)) return status;
    if (reconnecting) {
      restartDeadline ??= now() + 2 * 60_000;
      if (now() >= restartDeadline) break;
    } else {
      restartDeadline = undefined;
    }
    await pause();
  }
  return null;
}

function dateText(value: string | undefined, dateOnly = false): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return dateOnly ? date.toLocaleDateString("zh-CN") : date.toLocaleString("zh-CN");
}

function CoreUpdateSection({ host, fallbackVersion, onVersion, onRecovered }: {
  host: NativeHost;
  fallbackVersion?: string;
  onVersion: (version: string) => void;
  onRecovered: () => void;
}) {
  const [status, setStatus] = useState<CoreUpdateStatus | null>(null);
  const [pending, setPending] = useState<"check" | "apply" | "auto" | null>(null);
  const [tracking, setTracking] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [error, setError] = useState("");

  const receive = useCallback((next: CoreUpdateStatus) => {
    setStatus(next);
    onVersion(next.currentVersion);
  }, [onVersion]);

  useEffect(() => {
    let active = true;
    void host.request<CoreUpdateStatus>("update.status").then(next => {
      if (!active) return;
      receive(next);
      if (coreUpdateBusy(next.state)) setTracking(true);
    }).catch(cause => {
      if (active) setError(errorText(cause));
    });
    return () => { active = false; };
  }, [host, receive]);

  // Background auto-checks and top-bar update actions publish the same status, so
  // the card stays in sync without polling while the user is reading it.
  useEffect(() => host.subscribe<CoreUpdateStatus>("update", next => {
    if (next && typeof next === "object" && "state" in next) {
      receive(next);
      // A download started from the top-bar pill must make this card poll too,
      // otherwise it would sit on "正在下载" until the user reopens the page.
      if (coreUpdateBusy(next.state)) setTracking(true);
    }
  }), [host, receive]);

  useEffect(() => {
    if (!tracking) return;
    const monitor = new AbortController();
    let restartObserved = false;
    void watchCoreUpdate(host, (next, disconnected) => {
      restartObserved ||= disconnected;
      setReconnecting(disconnected);
      if (next) receive(next);
    }, monitor.signal).then(next => {
      if (monitor.signal.aborted) return;
      setReconnecting(false);
      setTracking(false);
      if (!next || (restartObserved && next.state === "idle")) {
        setError("暂时无法确认更新结果，请确认核心是否运行后再检查更新。");
      }
      if (next && (restartObserved || next.state === "updated" || next.state === "rolled_back")) onRecovered();
    });
    return () => monitor.abort();
  }, [host, onRecovered, receive, tracking]);

  async function check() {
    setPending("check");
    setError("");
    try {
      receive(await checkCoreUpdate(host));
    } catch (cause) {
      setError(errorText(cause));
      const next = await host.request<CoreUpdateStatus>("update.status").catch(() => null);
      if (next) receive(next);
    } finally {
      setPending(null);
    }
  }

  async function apply() {
    if (!status?.canApply) return;
    setPending("apply");
    setError("");
    try {
      const next = await host.request<CoreUpdateStatus>("update.apply");
      receive(next);
      setTracking(true);
    } catch (cause) {
      const next = await host.request<CoreUpdateStatus>("update.status").catch(() => null);
      if (next) {
        receive(next);
        if (coreUpdateBusy(next.state) || next.state === "updated" || next.state === "rolled_back") {
          setTracking(true);
        } else if (next.state !== "error") {
          setError(errorText(cause));
        }
      } else {
        setReconnecting(true);
        setTracking(true);
      }
    } finally {
      setPending(null);
    }
  }

  async function toggleAutoCheck(enabled: boolean) {
    if (pending !== null) return;
    setPending("auto");
    setError("");
    try {
      receive(await setCoreAutoCheck(host, enabled));
    } catch (cause) {
      setError(errorText(cause));
      const next = await host.request<CoreUpdateStatus>("update.status").catch(() => null);
      if (next) receive(next);
    } finally {
      setPending(null);
    }
  }

  const busy = pending !== null || tracking;
  const autoCheck = status?.autoCheck !== false;
  const checked = dateText(status?.checkedAt);
  const published = dateText(status?.publishedAt, true);
  const message = pending === "check" ? "正在检查更新…" : pending === "apply" ? "正在准备下载…" : coreUpdateText(status, reconnecting);
  const stateClass = error || status?.state === "error" || status?.state === "rolled_back" ? "faulted" : status?.state === "up_to_date" || status?.state === "updated" ? "ready" : "unknown";
  const previousError = status?.lastAttempt?.error;
  return <div className="ekds-group ekds-update" aria-label="版本更新">
    <div className="ekds-row">
      <span className="ekds-row-label">
        <strong>版本更新</strong>
        <small>当前版本 {status?.currentVersion || fallbackVersion || "未知"}{checked ? ` · 检查于 ${checked}` : ""}</small>
      </span>
      <button type="button" className="ekds-btn" onClick={() => void check()} disabled={busy}>检查更新</button>
    </div>
    <div className="ekds-update-status" role="status" aria-live="polite">
      <p className={`ekds-note ekds-state is-${stateClass}`}>{error || message}</p>
      {previousError && previousError !== message ? <p className="ekds-note">{previousError}</p> : null}
      {status?.state === "downloading" && status.totalBytes > 0 && !reconnecting ? (
        <progress value={Math.min(status.downloadedBytes, status.totalBytes)} max={status.totalBytes} aria-label="更新下载进度" />
      ) : null}
    </div>
    <div className="ekds-row">
      <span className="ekds-row-label">
        <strong>自动检查更新</strong>
        <small>后台定期检测 KimiCode+ 新版本；发现新版本时在主界面顶栏显示更新提示，不会自动重启客户端。</small>
      </span>
      <Toggle
        label="自动检查更新"
        checked={autoCheck}
        disabled={pending !== null}
        onChange={value => void toggleAutoCheck(value)}
      />
    </div>
    {status?.updateAvailable ? <>
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>新版本 {status.latestVersion}</strong>
          {published ? <small>发布于 {published}</small> : null}
          {status.releaseUrl ? <a className="ekds-update-link" href={status.releaseUrl} target="_blank" rel="noreferrer">查看发布页</a> : null}
        </span>
        {status.canApply ? (
          <button type="button" className="ekds-btn is-primary" onClick={() => void apply()} disabled={busy}>下载并更新</button>
        ) : null}
      </div>
      <p className="ekds-note ekds-update-note">
        {status.canApply ? "更新并重启核心后，预装增强插件会自动平滑升级，原有授权和数据不变。" : status.cannotApplyReason}
      </p>
      {status.releaseNotes ? <details className="ekds-update-notes">
        <summary>更新说明</summary>
        <pre className="ekds-log" tabIndex={0} aria-label="更新说明">{status.releaseNotes}</pre>
      </details> : null}
    </> : status?.releaseUrl && (error || status.state === "error" || status.state === "rolled_back" || (status.state === "up_to_date" && status.latestVersion)) ? (
      <p className="ekds-note ekds-update-note"><a className="ekds-update-link" href={status.releaseUrl} target="_blank" rel="noreferrer">查看发布页</a></p>
    ) : null}
  </div>;
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
  reloadContracts,
}: {
  host: NativeHost;
  contracts: ContractReport | null;
  onContractsChange: (next: ContractReport | null) => void;
  reloadContracts: () => Promise<ContractReport | null>;
}) {
  const { health } = useAuth();
  const [coreVersion, setCoreVersion] = useState("");
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
        // Shares the summary's single in-flight read instead of issuing a third.
        reloadContracts().catch(() => null),
      ]);
      setStatus(next);
      setDiagnostics(files);
      if (contractReport) onContractsChange(contractReport);
      setError("");
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [host, onContractsChange, reloadContracts]);

  const refreshAttach = useCallback(() => { void reload(); }, [reload]);

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

  const coreRunning = Boolean(coreVersion) || health?.status === "OK";
  const coreDiagnostics = <>
    <div className="ekds-group">
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>增强核心</strong>
          <small>{coreRunning ? `运行中 · 版本 ${coreVersion || health?.version || "未知"}` : "未连接，下面的状态可能不是最新的"}</small>
        </span>
        <span className={`ekds-state is-${coreRunning ? "ready" : "unknown"}`}>{coreRunning ? "运行中" : "未连接"}</span>
      </div>
    </div>
    <CoreUpdateSection host={host} fallbackVersion={health?.version} onVersion={setCoreVersion} onRecovered={reload} />
  </>;
  if (!status) {
    return <div className="ekds-body">
      {coreDiagnostics}
      <p className="ekds-note">{error || "正在读取宿主接入状态…"}</p>
    </div>;
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
    {coreDiagnostics}
    <div className="ekds-group">
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
          <small>{`${versionNote.replace(/。$/, "")}。该官方版本不在人工验收清单内，但界面与接口兼容检测已通过，可正常使用，无需额外操作。`}</small>
        </span>
        <span className="ekds-state is-warn">自动兼容</span>
      </div> : null}
      <div className="ekds-row">
        <span className="ekds-row-label">
          <strong>随 Kimi Code 启动自动接入</strong>
          <small>
            {status.available
              ? (status.agent.needsMigration
                ? "当前使用旧版自动接入。请关闭后重新开启，启用登录常驻。"
                : "开启后，登录系统时会启动增强核心。像平常一样打开 Kimi Code 即可自动接入。")
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
          {busy ? "处理中…" : status.agent.enabled ? "已开启" : "已关闭"}
        </button>
      </div>
      {status.agent.legacyCorePresent ? <p className="ekds-note">检测到手工配置的旧核心登录项。如需迁移，请先开启产品的自动接入，再停用旧手工项。产品常驻核心会在已有核心退出后接管。</p> : null}
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
      {status.platform === "windows" ? <div className="ekds-row">
        <div className="ekds-row-label">
          <strong>当前接入</strong>
          <WindowsAttachStatusPanel status={status} host={host} refresh={refreshAttach} />
        </div>
      </div> : null}
      {status.last && status.platform !== "windows" ? <div className="ekds-row">
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
