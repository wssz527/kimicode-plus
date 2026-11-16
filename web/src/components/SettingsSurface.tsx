import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../auth";
import { Icon } from "./Icon";
import { CacheSettings } from "./CacheSettings";
import { errorText } from "./workbench/pieces";
import { NativeSettings } from "./NativeSettings";
import type { NativeHost } from "../native";
import { BrowserPage } from "../pages/BrowserPage";
import { CredentialsPage } from "../pages/CredentialsPage";
import { ModelsPage } from "../pages/ModelsPage";
import { PluginsPage } from "../pages/PluginsPage";
import { SettingsPage } from "../pages/SettingsPage";
import { UsagePage } from "../pages/UsagePage";
import { WorkspacePage } from "../pages/WorkspacePage";
import "../styles/native-settings-surface.css";

const SECTIONS = [
  { id: "overview", label: "概览", hint: "核心与增强状态" },
  { id: "attach", label: "接入与自动恢复", hint: "宿主发现与后台接入" },
  { id: "planning", label: "规划与交接", hint: "来源、预设与跨会话交接" },
  { id: "browser", label: "浏览器权限与数据", hint: "网站操作权限与导入记录" },
  { id: "native", label: "原生会话配置", hint: "工具、子代理模型与权限规则" },
  { id: "workspace", label: "工作台与布局", hint: "会话引用、压缩阈值与 Worktree" },
  { id: "cache", label: "缓存与额度", hint: "保活、自动压缩与恢复续跑" },
  { id: "usage", label: "用量", hint: "实际记录、估算价格与额度" },
  { id: "models", label: "供应商与模型", hint: "目录、能力字段与回滚" },
  { id: "credentials", label: "凭据库", hint: "加密保存的密钥引用" },
  { id: "mount", label: "插件挂载", hint: "Monitor 与故障隔离" },
  { id: "enhancement", label: "增强管理", hint: "模块与安全模式" },
] as const;
type SectionId = typeof SECTIONS[number]["id"];

export function SettingsWindow({ host }: { host: NativeHost }) {
  const [openId, setOpenId] = useState<SectionId | null>(null);
  const returnTo = useRef<SectionId | null>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const rows = useRef(new Map<SectionId, HTMLButtonElement>());

  useEffect(() => {
    if (openId) backRef.current?.focus();
    else if (returnTo.current) rows.current.get(returnTo.current)?.focus();
  }, [openId]);

  if (!openId) {
    return <div className="ekds">
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
        {openId === "overview" ? <OverviewSection /> : null}
        {openId === "attach" ? <AttachSection host={host} /> : null}
        {openId === "planning" ? <PlanningSection host={host} /> : null}
        {openId === "browser" ? <div className="ekds-embed"><BrowserPage /></div> : null}
        {openId === "native" ? <div className="ekds-embed"><NativeSettings embedded /></div> : null}
        {openId === "workspace" ? <div className="ekds-embed"><WorkspacePage /></div> : null}
        {openId === "cache" ? <CacheSettings host={host} /> : null}
        {openId === "usage" ? <div className="ekds-embed"><UsagePage /></div> : null}
        {openId === "models" ? <div className="ekds-embed"><ModelsPage /></div> : null}
        {openId === "credentials" ? <div className="ekds-embed"><CredentialsPage /></div> : null}
        {openId === "mount" ? <MountSection /> : null}
        {openId === "enhancement" ? <div className="ekds-embed"><SettingsPage /></div> : null}
      </div>
    </div>
  </div>;
}

function OverviewSection() {
  const { health } = useAuth();
  const modules = health?.modules ?? [];
  return <>
    <dl className="ekds-facts">
      <div><dt>增强核心</dt><dd>{health?.status === "OK" ? "运行中" : "等待连接"}</dd></div>
      <div><dt>宿主契约</dt><dd>通过版本与 DOM 校验后启用</dd></div>
      <div><dt>同屏会话</dt><dd>默认关闭，仅在主动添加后挂载</dd></div>
      <div><dt>Monitor</dt><dd>会话侧栏底部，不进入聊天上下文</dd></div>
    </dl>
    <div className="ekds-group">
      {modules.length === 0
        ? <p className="ekds-empty">还没有读到模块状态。</p>
        : modules.map(module => <div className="ekds-row" key={module.id}>
          <span className="ekds-row-label"><strong>{module.id}</strong><small>{module.message || "可用"}</small></span>
          <span className={`ekds-state is-${module.state.toLowerCase()}`}>{module.state}</span>
        </div>)}
    </div>
    <p className="ekds-note">增强操作在主界面，这里只管理配置与运行状态。</p>
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
  };
  agent: { enabled: boolean; path?: string; error?: string };
  last?: { status?: string; error?: string; consentRequired?: boolean; hostPid?: number; appVersion?: string; daily?: boolean };
  available: boolean;
  unavailableReason?: string;
};

const FUSE_TEXT: Record<string, string> = {
  ready: "允许临时调试端点",
  "signal-disabled": "已禁用调试信号，无法接入",
  "unknown-layout": "无法识别该版本的调试开关",
};

function AttachSection({ host }: { host: NativeHost }) {
  const [status, setStatus] = useState<AttachStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [consent, setConsent] = useState(false);

  const reload = useCallback(async () => {
    try {
      setStatus(await host.request<AttachStatus>("attach.status"));
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
  return <>
    <p className="ekds-note">增强层只在客户端运行期间接入。开启下面的后台接入后，客户端重启或刷新会由系统在文件变化时触发一次接入，不轮询进程。</p>
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
  </>;
}

function PlanningSection({ host }: { host: NativeHost }) {
  return <>
    <p className="ekds-note">本地和网页来源互斥使用；切回另一种来源不会清空已有草稿。</p>
    <div className="ekds-group">
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>规划面板</strong><small>入口位于官方“侧边聊天”下方，右侧停靠沿用官方拖拽，也可以弹出为独立窗口。</small></span>
      </div>
      <dl className="ekds-facts is-inset">
        <div><dt>默认预设</dt><dd>纯净规划</dd></div>
        <div><dt>项目审查</dt><dd>上下文与 Skill，工具只读</dd></div>
        <div><dt>全功能</dt><dd>可写操作仍需逐次批准</dd></div>
      </dl>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>网页聊天</strong><small>在当前面板或独立窗口打开网页，提取讨论内容后编辑，再交给 Code。</small></span>
        <button type="button" className="ekds-btn" onClick={() => void host.openBrowser("https://www.kimi.com/", "exec")}>打开网页聊天</button>
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>交接</strong><small>支持多选消息、编辑预览、新建或已有会话，以及普通、Plan、Tower 三种模式。已有会话不会被增强默认模型暗改。</small></span>
      </div>
    </div>
  </>;
}

function MountSection() {
  return <>
    <p className="ekds-note">通过受控兼容层复用第三方能力，不运行上游安装器，不修改官方程序包体。</p>
    <dl className="ekds-facts">
      <div><dt>kimi-code-monitor 来源</dt><dd>bfjnbvf/kimi-code-monitor · MIT</dd></div>
      <div><dt>固定版本</dt><dd><code>1d529f0b424a708971cfac097238df444f3b5beb</code></dd></div>
      <div><dt>挂载位置</dt><dd>aside.side 的会话列表底部、side-footer 之前</dd></div>
      <div><dt>更新策略</dt><dd>锁定当前提交，不自动跟随浮动分支</dd></div>
    </dl>
    <div className="ekds-embed"><PluginsPage /></div>
  </>;
}
