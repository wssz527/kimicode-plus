# 原生接入门槛与当前支持边界

Go Core 仍可独立运行。当前原生路径已在隔离 Kimi Code 1.3.5 / Electron 45.1.0 中完成版本化 CDP 附着、主进程代持核心凭据、正式 loopback RPC、KAP（官方本地会话 API）的会话读取/发送/事件、规划流式回复、网页内容提取、真实 Code 交接、多会话和内嵌设置。它不是官方 SDK；宿主版本或 DOM 失配时降级停用。

**政策更新（2026-11-12，用户决定）**：本软件定位为本地个人开源工具。经用户显式授权，可只读复用指定官方客户端配置与凭据，导入增强程序自己的加密库，不写回官方存储。接入优先走官方公开面与本地 API；不改官方包体。界面与插件经显式授权使用版本受限的运行时接入，失配降级。

当前开发主实例未被附着、注入、重启或修改。验收使用独立 profile、独立 KIMI_CODE_HOME、独立 EKD 数据目录、独立测试工作区和本机合成模型/网页服务。三项自动化默认关闭；真实账号上的保活、压缩和额度恢复仍未验收。

## 同窗页面与宿主入口

官方界面以 Vue #app 为根，使用 data-color-scheme 与字体变量。`internal/desktop/assets/renderer.js` 在官方右侧 `pt-body` 挂载规划，在主聊天 `.chat-main` 挂载多会话，在侧栏 `side-footer` 前挂载 Monitor 和设置入口。统一“规划”入口默认网页，来源与配置位于菜单；入口分别克隆已有会话侧栏行和空会话工具菜单行。容器属性与 CSS 持续隐藏 Vue 重建的官方子节点，切回原生工具或单会话时释放。内容采用 Shadow DOM 隔离 CSS，并同步宿主主题。

入口注入按容器判重：`syncNativeEntries` 遍历每个可见的宿主菜单容器（空面板 `.sit`、`+` 下拉 `.pl`），在该容器内查同动作入口，多于一个则删除多余项，锚点行被重建则重新插入。判断依据是容器内是否已有入口，不使用全局单引用加 `isConnected`，因此官方重渲染任一容器后入口都能恢复且不会重复。

侧栏开合由 `layout.acquireSidebarCollapse()` 引用计数管理：第一次 acquire 时记录当前开合状态，最后一次 release 时按记录恢复；本来就折叠的侧栏不会被展开。规划和同屏各持一份 release，互不影响。

设置嵌入官方设置对话框，KimiCode+ 标签在“已归档”前，不默认创建独立 BrowserWindow。首页为单层列表与子页返回，复用已有核心页面、开关和请求链路。原生标签恢复、动态重附着、窄宽度和保存读回已实机通过。侧栏 Monitor 不依赖右侧工具起始页；侧栏宿主重建时先卸载旧 Monitor，避免遗留订阅和定时器。

`web/src/host-entry.tsx` 同时保留独立管理页的 mountEnhancement 与原生 mountSurface。宿主对象按挂载实例经 React Context 注入（`HostScopeContext` / `useHost`），不再有模块级单例，卸载任一面板不会清空其他面板的宿主。核心传输与内嵌导航按挂载数引用计数，最后一个面板卸载后才断开。`host.ts` 的 `releaseHost(expected)` 只在连接仍是自己安装的那个时才清除。

渲染进程只保留一条 `events.poll` 长轮询：`createEventChannel` 在 renderer 中创建一次，所有面板的 host 共享并在进程内广播，面板关闭时只退订自己的 scope。全部面板关闭后循环自然结束。空页且往返过快时让出 50ms，避免等待被提前切断时自旋。设置中的 Core 数据经 `core.fetch` 由主进程转发，renderer 不持有核心 Bearer token。每个 WebContents 使用独立 bridge key，服务端校验 loopback、Origin、WebContents 归属、协议和动作名，卸载时清理连接与界面。请求 ID 在 `crypto.randomUUID()` 不可用的 opaque origin 中使用随机回退；布局、权限和缓存保存已实机通过。

## 内置浏览器入口（2026-11-12 的历史验证）

官方桌面端内置浏览器（kimi.browser/1.0.0，WebContentsView）可以直接承载本程序的 loopback 页面。源码核对：浏览器工具的 URL 校验只检查 http(s) 格式与长度，不拦截私有地址；web fetch 工具另行拦截 loopback，两者策略不同。

当时实测：通过 browser.create_tab 打开 runtime.json 的 pairingUrl，配对自动完成，工作台全部模块渲染，主题跟随系统深色，页内导航（/models）与 Go API 数据读取正常。全程未改包体、未注入、未读取官方凭据。该记录仅说明早期浏览器入口，不代表当前原生菜单能力。浏览器入口的配对凭据为内存级，刷新或重开需重新配对，宿主上下文不会自动传入。

## 运行时接入器（KCT 式，常驻面板与插件宿主）

当前产品接入器使用用户已授权的运行时路径，不改包体：

1. 发现唯一主进程，核对进程身份（uid、启动时间、可执行路径）、运行镜像 inode、Electron fuse 与 renderer DOM 契约。任一项不满足即停用，不盲目注入。
2. 向运行中的 Kimi Code 主进程发送 SIGUSR1，触发 Node inspector 监听 127.0.0.1:9229（Node 内置能力，无需启动参数）。
3. 端口出现后先用 `lsof` 确认监听者就是目标 PID，再经 `/json/list` 取 node 目标的 webSocketDebuggerUrl。
4. 第一条 inspector 命令即安装 12 秒自动关闭的 deadline，并校验 `process.pid`、`process.type` 与 Electron 版本。
5. 钩子借主进程 webContents 向 renderer 注入 UI，并在 `did-finish-load` 后重新挂载；卸载与核心重启时清理入口、视图和连接。
6. 关闭时清除 deadline 并自宿主内部关闭 inspector，随后最多轮询 15 秒确认端口真的关闭；关不掉时明确报告，而不是留下调试端口。

EKD 由 `internal/desktop/inject.go`、`cdp.go`、`inspector_fuse.go` 和内嵌 JS 实现该机制。仅接受明确列出的宿主版本；当前契约为 1.3.4/1.3.5 + Electron 45.1.0 + DOM revision 1。

**不再要求 `--user-data-dir`**。这一条曾把接入限制成"只能接隔离副本"，而从程序坞正常启动的实例没有该参数。现在的规则：

- `--host-profile` 为空时，「接入」（`attach --kimi-home <home>`）与自动发现都接受当前唯一主进程，并把它识别为**日常实例**。
- 日常实例必须由用户在设置里显式同意（`--allow-daily-instance` / 设置页的同意勾选）。没有这份同意时返回"需要授权"，绝不静默附着。
- 带 `--user-data-dir`、`--remote-debugging`、`--inspect` 的实例一律视为调试实例，不自动接入。

### 可选的 LaunchAgent 事件驱动接入

`launch_agent.go` 提供默认关闭、用户显式开启的常驻接入：

- plist 使用 `WatchPaths` 指向 `KIMI_CODE_HOME/server/instances` 与 `server.token`。这两个文件在官方客户端每次启动时被重写，因此重启或刷新会触发一次接入；**不使用 `StartInterval`，不轮询进程**。
- 同时设置 `RunAtLoad`（开启时客户端已在运行也能立刻接入）与 `ThrottleInterval 5`（避免一次启动写出多份 profile 文件时反复拉起）。
- 安装采用"写 `.next` 再 rename"的原子替换；旧的已加载 job 先 `bootout` 再 `bootstrap`，避免 EEXIST。卸载幂等。
- 状态判定要求"plist 存在**且** job 已加载"，只有两者都成立才在界面上显示为已开启。
- 每次运行的结论写入 `attach-state.json`；需要授权时该文件带 `consentRequired: true`，设置页据此给出说明而不是只显示一个失败状态。

命令行入口：

```
enhanced-kimi attach --kimi-home <home> --core-url <url> --pairing-token <token> [--host-profile <path>] [--allow-daily-instance]
enhanced-kimi attach --kimi-home <home> --core-url <url> --pairing-token <token> --install
enhanced-kimi attach --uninstall
enhanced-kimi attach --status --data-dir <dir>
```

早期 1.3.3 独立 BrowserWindow 与 1.3.4 设置内嵌样例是历史验证。当前 1.3.5 已使用正式本地 RPC，不再经 console-message 演示桥。真实 KAP 由 Go 客户端读取指定隔离 KIMI_CODE_HOME 下的实例记录和 `server.token`，访问 `/api/v1`；不会扫描或接管其他 profile。实例记录和凭据在连接失败或 401 时重新解析一次再重试，非幂等请求不会被盲目重发。

## 网页、自动化与 Monitor

网页由增强程序创建 WebContentsView 或独立窗口承载。当前支持选区/正文提取、编辑预览和向真实 Code 会话交接，不支持向第三方网站自动填写/发送消息或读取回复。来源切换保留草稿，菜单、会话选择及设置弹层出现时隐藏网页子视图。右侧滑入过渡只改变位置、不触发 ResizeObserver，因此视图坐标另在过渡运行期间同步，静止后停止。真实视图可见性和坐标已核对。

原生多会话主窗格跟随官方侧栏，同一添加入口逐次追加至三个，已显示会话通过交换避免重复。输入草稿、模型、模式、权限和附件按会话集中保存。系统文件选择返回路径，经 Go 校验最多 16 个本机常规文件后复用 KAP prompt 发送；官方消息保留时间和附件元数据。合成文件真实发送与读回已通过，系统文件对话框仅使用确定性返回值验证调用，未 GUI 点选；这不等于网站上传或旧 Outbox 上传。

`automation_scheduler.go` 每五秒检查状态；三项默认关闭，安全模式下不执行。开启后才同步官方会话列表，到期才读取完整 turns、审批与 Goal。保活使用最小“继续”且保留 Plan/Tower 模式；压缩要求官方 context_tokens/context_limit 明确，`:compact` ACK 与 `compaction.completed` 分开记录。原生会话恢复要求最后一轮明确 429、无新用户接管、Goal 未停止且硬预算未耗尽，并核对官方额度窗口。规划线程的受控恢复复用原 revision，不追加重复用户消息。pending/acknowledged 在核心重启后转为 UNKNOWN，不重放。监听器支持显式撤销，同一上下文的已撤销/UNKNOWN/已完成状态不会被重新排队，显式重新监听才可建立新候选。

Monitor 位于会话侧栏，默认约 233px 的紧凑布局，详情折叠。界面参考 `bfjnbvf/kimi-code-monitor@1d529f0b424a708971cfac097238df444f3b5beb`，MIT 归属与全文随构建交付，不启动上游采集器。额度按官方 `/oauth/usage` 的 kind 和 quota 结构区分 AVAILABLE、ERROR 与 UNKNOWN，只有有效数据才显示 5h/本周已用比例；读取错误不保留旧比例。输入、输出、速度和缓存命中没有样本时显示 `--`，诊断保留 UNKNOWN。真实自动化回执单独放在详情中，不冒充缓存命中率。25/29/30 分钟只是社区经验提醒，不是官方 TTL。

## 可执行的合成配对桥

`internal/desktop/pairing.go` 的 NewIsolatedTestHost / PairIsolatedHost 是独立 httptest 协议，仅报告只读合成能力。身份、版本、challenge、scope、Host/Origin、有效期及未授权拒绝已有回归。它不是官方 SDK 或生产配对入口，也不会启动第二个官方实例。

## 最小隔离实机验证

原生验收使用与当前开发实例隔离、可丢弃的测试宿主、独立 profile、独立 KIMI_CODE_HOME 和独立 EKD 数据目录。当前可重复脚本位于 `../work/ekd-final-integration/`。

1. `verify-review-fixes.mjs` 是当前 UI 验收入口，检查动态容器隔离、空会话入口、统一规划默认网页、两窗格追加为三窗格、官方侧栏切换/交换、草稿保留、深浅色和窄宽度；还覆盖设置 11 项列表、标签顺序/恢复、缓存撤销/默认/保存读回、紧凑 Monitor。旧 `verify-native-ui.mjs` 和 `verify-native-flow.mjs` 仅作早期记录。
2. `verify-settings-flow.mjs` 验证布局与浏览器权限保存/恢复、真实模型目录加载；`verify-planner-flow.mjs` 验证流式规划、停止、工具审批、多选、编辑预览与 Code 交接。
3. `verify-multi-session.mjs` 验证三窗格并行更新、独立草稿/模型/模式、只停止目标会话及真实 Read 审批，测试后的权限规则恢复。
4. `verify-web-flow.mjs` 验证本地合成网页的真实 WebContents 选区提取、编辑预览、视图坐标与 Code 交接，不证明网站内自动聊天。
5. `verify-attachment-flow.mjs` 验证附件加号、待发条目、权限选择、本机文件真实 KAP 发送、官方附件/时间读回与引用回复。系统文件对话框使用临时确定性返回值，finally 恢复方法；未实际 GUI 点选。
6. `verify-recovery-flow.mjs` 验证手动监听、立即检查、撤销监听和恢复官方单会话。脚本期间暂关三项自动化，finally 恢复原值；隔离环境原有 recovery.enabled=true，不把此值误记成产品默认值。不制造真实限流或启用真实账号自动续跑。
7. 官方配置写入只针对隔离 HOME 与合成 provider。官方无 CAS 时，不把本地 revision 说成跨进程事务；未知回执不重发。结束后关闭额外 surface，恢复布局、权限与窗口最小尺寸等测试数据。

主 WebContents 截图不包含子 WebContentsView；macOS `screencapture` 和 Electron desktopCapturer 在当前权限下未取得完整窗口合成图。已有规划、多会话、设置与恢复面板的主视图截图，以及真实网页子视图截图，但不声称具备完整窗口合成视觉证据。

## 各能力的实际范围

| 能力 | 当前已实现 | 未完成的原生部分 |
|---|---|---|
| 模型中心 | 公开目录、供应商列表 GET、本地来源/覆盖/事务 | 官方配置同步、原生模型菜单 |
| 用量 | 独立账本、用户导入、轻量聊天与本地网关响应入账、账号归因 | 官方 Token 事件接入 |
| 双向任务 | 原生 `handoff` 向真实 Code 会话发送文本，`session.send` 发送本机文件；旧 Outbox 保留附件、幂等与 Mock 回执 | 系统文件选择窗口 GUI 点选、网站内发送/回复/上传、两条链路统一 |
| 聊天 | Go 四协议轻量聊天；原生规划有流式、停止、工具审批；本地网关通过官方 CLI 合成文本与 Read 回合 | 真实付费供应商验收；轻量聊天仍无流式和工具 |
| 凭据 | 口令解锁 AES-GCM 库、secretRef、显式路径只读导入 | refresh_token 刷新、OS Keychain；不回写官方存储 |
| 内置浏览器 | 自建 WebContentsView/窗口、选区/正文提取、编辑并交接 Code，视图可见性与坐标已核对 | 网站内对话、上传、登录态恢复与完整窗口合成截图 |
| 压缩与保活 | 原生后台调度、真实 KAP 请求、意图/回执、Goal/审批/竞争保护；合成回归通过 | 真实账号上的自动动作验收 |
| 多会话/Tower | 最多三个 KAP 会话，逐次追加、主窗格跟随与交换去重；独立草稿/模型/模式/权限/附件/停止/审批；本地 Worktree 和布局偏好 | Reviewer 仍只是本地偏好 |
| KCT/Monitor | Go 指令整理、兼容事件导入；紧凑侧栏、官方额度状态、MIT 界面归属与许可，回执与命中率分开 | 完整真实速度/缓存观测、真实账号额度验证、外部插件市场 |
| 设置 | 单层 11 项列表内嵌官方设置对话框，标签顺序/恢复、原生配置、缓存/权限/布局保存与读回 | 更多真实账号和平台组合 |
| 发布 | 五平台纯 Go 交叉构建、macOS ARM64 运行 | 其他平台原生运行、签名公证、Windows ACL |

单实例锁只保护增强软件自己的数据目录，不能代表官方会话所有权。官方升级或适配器失配只停用相应能力，核心、本地配置和历史仍能启动。
