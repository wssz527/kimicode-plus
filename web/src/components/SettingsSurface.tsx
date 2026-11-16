import { useEffect, useRef, useState } from "react";
import { useAuth } from "../auth";
import { Icon } from "./Icon";
import { CacheSettings } from "./CacheSettings";
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
