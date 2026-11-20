# 原生接入门槛与当前支持边界

Go Core 仍可独立运行。当前原生路径已在隔离 Kimi Code 1.3.5、1.3.7 / Electron 45.1.0 中完成契约检测式 CDP 附着、主进程代持核心凭据、正式 loopback RPC、KAP（官方本地会话 API）的会话读取/发送/事件、规划流式回复、网页内容提取、真实 Code 交接、多会话和内嵌设置。它不是官方 SDK；注入不再按官方版本号白名单放行，而是按安全门槛和逐项界面/接口契约放行，单项失配时只停用对应功能。

**政策更新（2026-11-19，用户决定）**：本软件定位为本地个人开源工具。经用户显式授权，可改写官方客户端的用户数据、配置和凭据（改写前备份、原子写入、失败回滚），但仍不改官方程序包体。接入优先走官方公开面与本地 API；界面与插件经显式授权使用基于契约探针的运行时接入，单项失配即降级停用对应入口。

当前开发主实例未被附着、注入、重启或修改。验收使用独立 profile、独立 KIMI_CODE_HOME、独立 EKD 数据目录、独立测试工作区和本机合成模型/网页服务。三项自动化默认关闭；真实账号上的保活、压缩和额度恢复仍未验收。

## 同窗页面与宿主入口

官方界面以 Vue #app 为根，使用 data-color-scheme 与字体变量。`internal/desktop/assets/renderer.js` 向插件接口 v2 提供右侧 `pt-body` 面板、工具菜单入口及侧栏锚点。规划插件默认关闭，启用后注册一个“规划”入口，打开时才创建动态标签。内容使用 Shadow DOM 隔离 CSS，并跟随宿主主题。设置只嵌入官方设置对话框，不在侧栏克隆入口。旧多屏会话仍可按附加功能开关挂载到 `.chat-main`，其插件版 A2 已延期。旧内置规划挂载和演示工作台已移除。

入口注入按容器判重：`syncNativeEntries` 遍历每个可见的宿主菜单容器（空面板 `.sit`、`+` 下拉 `.pl`），在该容器内查同动作入口，多于一个则删除多余项，锚点行被重建则重新插入。判断依据是容器内是否已有入口，不使用全局单引用加 `isConnected`，因此官方重渲染任一容器后入口都能恢复且不会重复。

侧栏开合由 `layout.acquireSidebarCollapse()` 引用计数管理：第一次 acquire 时记录当前开合状态，最后一次 release 时按记录恢复；本来就折叠的侧栏不会被展开。规划和同屏各持一份 release，互不影响。

设置按官方层级嵌入官方设置对话框，不默认创建独立 BrowserWindow：多账号入口挂载到官方「账户」页顶部，权限规则、子代理模型池、子代理时限和单轮步数上限挂载到官方「智能体与会话」页对应官方小节下方（并与官方 `secondary_model` 单选双向同步），会话并排实验开关挂载到官方「实验室」页，模型能力留在官方「模型供应商」页；KimiCode+ 标签在「已归档的会话」前，直接平铺「缓存」「增强插件」「状态与诊断」三节（单增强插件详情保留一层「返回增强插件」子页），不注入官方「插件」页。切换官方标签页或关闭弹层时自动卸载注入节点，官方某页契约探针失效时仅停用该处挂载并将受影响内容临时回落显示在 KimiCode+ 标签页里。侧栏 Monitor 是预装增强插件，挂载点由插件运行时自己的锚点管理，宿主重建时随插件停用一起释放，不留订阅和定时器。

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

EKD 由 `internal/desktop/inject.go`、`cdp.go`、`contracts.go`、`inspector_fuse.go` 和内嵌 JS 实现该机制。接入不再依赖官方版本号白名单：只要安全门槛（唯一主进程身份、Electron fuse 允许 inspector、渲染协议 `app://renderer/`）通过即允许附着；`1.3.4`/`1.3.5`/`1.3.7` 仅作为已人工验证记录，遇到未验证版本会在「状态与诊断」标注"该版本未经人工验证，契约检测通过"。各界面注入点与官方本地接口（`/config`、`/providers`、`/models`、`/oauth/usage`、`/sessions`）逐项探测，任一项失效仅停用并清理对应功能，其余功能照常运行。

**不再要求 `--user-data-dir`**。这一条曾把接入限制成"只能接隔离副本"，而从程序坞正常启动的实例没有该参数。现在的规则：

- `--host-profile` 为空时，「接入」（`attach --kimi-home <home>`）与自动发现都接受当前唯一主进程，并把它识别为**日常实例**。
- 日常实例必须由用户在设置里显式同意（`--allow-daily-instance` / 设置页的同意勾选）。没有这份同意时返回"需要授权"，绝不静默附着。
- 带 `--user-data-dir`、`--remote-debugging`、`--inspect` 的实例一律视为调试实例，不自动接入。

### Windows 启动器与插件主进程桥

Windows 启动器启动已校验的官方客户端时，在 Electron 开关允许的情况下使用临时环回 Node inspector。核对进程、监听属主、目标类型后，安装与 macOS 共用的主进程桥，并关闭 inspector。后续由主进程创建插件窗口、网页视图和独立菜单条目。兼容路径继续通过已验证的 Chromium 渲染端口接入。它保留右侧面板等渲染能力，并在安装预览和运行时报告三项独立能力的限制。现有 Windows 实例若只有渲染进程接入，正常退出客户端后通过启动器重新打开即可尝试主进程接入。

### 可选的 LaunchAgent 事件驱动接入

`launch_agent.go` 提供默认关闭、用户显式开启的常驻接入：

- plist 使用 `WatchPaths` 指向 `KIMI_CODE_HOME/server/instances` 与 `server.token`。这两个文件在官方客户端每次启动时被重写，因此重启或刷新会触发一次接入；**不使用 `StartInterval`，不轮询进程**。
- 同时设置 `RunAtLoad`（开启时客户端已在运行也能立刻接入）与 `ThrottleInterval 5`（避免一次启动写出多份 profile 文件时反复拉起）。
- 安装采用"写 `.next` 再 rename"的原子替换；旧的已加载 job 先 `bootout` 再 `bootstrap`，避免 EEXIST。卸载幂等。
- 状态判定要求"plist 存在**且** job 已加载"，只有两者都成立才在界面上显示为已开启。
- **plist 的 `ProgramArguments` 携带接入所需的全部参数，包括 `--core-url` 与 `--pairing-token`。** launchd 唤醒 agent 时没有任何其他输入渠道，缺任一项都会让每次唤醒失败，而界面仍显示"已开启"。因此这两项与 plist、程序化安装共用同一个参数构造函数（`AttachArguments`），plist 渲染结果与 CLI 参数集合由回归测试逐项比对，防止再次漂移。凭据排在参数末尾，避免含 `=`/`-` 的 token 被当作 flag。
- **plist 含配对凭据，权限固定 `0600`（仅属主可读写）**；仅在用户显式开启后台接入时写入 `~/Library/LaunchAgents/`，卸载时一并删除。凭据不写入日志；`attach.log` 与 `attach-state.json` 只记录结果与错误原因。
- 安装前先用真实 flag 集合解析一遍渲染出的参数；解析或必填项校验失败时**不写入** plist，避免留下一个必然失败的 agent。

### Electron fuse 位次与版本绑定

- Node inspector 信号 fuse 的**位次是 Electron 源码顺序的属性，不是从二进制里推得出来的**，跨大版本可能移动。因此位次按 Electron 版本显式绑定在 `VerifiedFuseLayouts` 表中（当前仅 `45.1.0`：block version 1、9 个 fuse、信号位次 3），并与宿主契约共用同一个 `SupportedElectron` 常量。
- 表中没有的 Electron 版本**失败关闭**，返回"该 Electron 版本未经验证 fuse 布局"，不猜测新布局。声明数量、block version 与位次表不一致时同样判为未知布局，而不是照旧读取可能已指向别的 fuse 的位置。
- 读取不再整文件加载（本机 `Electron Framework` 约 207MB，而 `attach.status` 会被设置页反复轮询）：改为 1MB 分块流式扫描并保留跨块 carry，内存与文件大小无关；同一文件（size + mtime）与同一 Electron 版本的结果缓存，文件变化即失效。
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

规划插件通过 `browser:view` 创建隔离 WebContentsView，视图按插件、renderer 实例和 viewId 归属。打开、导航和核心登记清理按视图串行处理。关闭时先中断由当前令牌、实例和窗口拥有的加载视图，避免网页响应阻塞释放。加载结束后核对宿主仍存活，失败时回滚核心登记。支持五站消息适配器、选区/正文回退、编辑预览和向真实 Code 会话交接。预览绑定规范化后的内容、目标和新会话字段，字段变化或旧响应均不能沿用确认。反向支持把摘要或 Diff 填入网页输入框，不自动发送。真实站点登录后的解析和回填仍未验收。来源切换保留草稿，菜单和弹层出现时隐藏网页子视图，实际可见性和坐标分别验证。

原生多会话主窗格跟随官方侧栏，同一添加入口逐次追加至三个，已显示会话通过交换避免重复。输入草稿、模型、模式、权限和附件按会话集中保存。系统文件选择返回路径，经 Go 校验最多 16 个本机常规文件后复用 KAP prompt 发送；官方消息保留时间和附件元数据。合成文件真实发送与读回已通过，系统文件对话框仅使用确定性返回值验证调用，未 GUI 点选；这不等于网站上传或旧 Outbox 上传。

`automation_scheduler.go` 每五秒检查状态；自动化默认关闭，安全模式下不执行。开启后才同步官方会话列表，到期才读取完整 turns、审批与 Goal。后台等待保活在等待后台子代理时经 `prompts:steer` 发送短提示 `[保活]无需操作，继续等待` 并保留 Plan/Tower 模式；空闲会话保活按供应商类型推导默认间隔（订阅 30 分钟 / 每 25 分钟、开放平台 5 分钟 / 每 4 分钟、其他默认不自动保活），发送 `[保活]仅回复1` 并在该轮结束后调用官方 `:undo` 撤回该轮；压缩在保活结束后才触发，要求官方 context_tokens/context_limit 明确，`:compact` ACK 与 `compaction.completed` 分开记录。原生会话恢复要求最后一轮明确 429、无新用户接管、Goal 未停止且硬预算未耗尽，并核对官方额度窗口。规划线程的受控恢复复用原 revision，不追加重复用户消息。pending/acknowledged 在核心重启后转为 UNKNOWN，不重放。监听器支持显式撤销，同一上下文的已撤销/UNKNOWN/已完成状态不会被重新排队，显式重新监听才可建立新候选。

Monitor 位于会话侧栏，默认约 233px 的紧凑布局，详情折叠。界面参考 `bfjnbvf/kimi-code-monitor@1d529f0b424a708971cfac097238df444f3b5beb`，MIT 归属与全文随构建交付，不启动上游采集器。额度按官方 `/oauth/usage` 的 kind 和 quota 结构区分 AVAILABLE、ERROR 与 UNKNOWN，只有有效数据才显示 5h/本周已用比例；读取错误不保留旧比例。输入、输出、速度和缓存命中没有样本时显示 `--`，诊断保留 UNKNOWN。真实自动化回执单独放在详情中，不冒充缓存命中率。保活默认使用的供应商缓存时长（订阅 30 分钟、开放平台 5 分钟）用于推导保活间隔并允许在高级设置中覆盖，不是官方承诺的固定 TTL。

## 可执行的合成配对桥

`internal/desktop/pairing.go` 的 NewIsolatedTestHost / PairIsolatedHost 是独立 httptest 协议，仅报告只读合成能力。身份、版本、challenge、scope、Host/Origin、有效期及未授权拒绝已有回归。它不是官方 SDK 或生产配对入口，也不会启动第二个官方实例。

## 最小隔离实机验证

原生验收使用与当前开发实例隔离、可丢弃的测试宿主、独立 profile、独立 KIMI_CODE_HOME 和独立 EKD 数据目录。A3 当前脚本为项目内 `work/a3-stage/verify-a3.mjs`，由 `A3_STAGE_DIR` 指定独立运行目录，逐阶段写入报告并绑定源码及二进制 SHA-256。当前候选结果见 `work/a3-stage-final/a3-acceptance-report.json`。

以下 `work/ekd-final-integration/` 脚本是插件化前的历史验收记录，不能作为旧规划入口仍存在或 A2 插件已完成的依据。

1. `verify-review-fixes.mjs` 是当时的 UI 验收入口，检查动态容器隔离、空会话入口、统一规划默认网页、两窗格追加为三窗格、官方侧栏切换/交换、草稿保留、深浅色和窄宽度；还覆盖设置的 5 项列表与独立管理窗口跳转、标签顺序/恢复、缓存撤销/默认/保存读回、紧凑 Monitor。旧 `verify-native-ui.mjs` 和 `verify-native-flow.mjs` 仅作早期记录。
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
| 设置 | 单层 5 项列表加一条独立管理窗口跳转行，内嵌官方设置对话框；标签顺序/恢复、原生配置保存后经 GET 读回、缓存保存读回、附加功能开关即时保存、只读诊断（接入日志尾部与开源许可）| 更多真实账号和平台组合；管理窗口的实机深浅色截图 |
| 发布 | 五平台纯 Go 交叉构建、macOS ARM64 运行 | 其他平台原生运行、签名公证、Windows ACL |

单实例锁只保护增强软件自己的数据目录，不能代表官方会话所有权。官方升级或适配器失配只停用相应能力，核心、本地配置和历史仍能启动。

## 缓存与额度（M2）

数据来自本机 `KIMI_CODE_HOME/sessions/<桶>/<会话>/` 的只读解析，不写任何官方文件。`KIMI_CODE_HOME` 存在时以它为根，否则用默认目录。

### 读取与存储

- 按文件 inode + device 加偏移量增量读取；文件被替换（inode/device 变化）或截断（size 小于已读偏移）时从头重读。
- 无 fsnotify 依赖（`go.mod` 确认不存在），用定时扫描，间隔不短于 5 秒；该下限在 Store 内部二次强制，调用方无论怎么 tick 都不会更密。
- 跨读取的未完成行在同一次扫描内拼接继续读，只有文件末尾真正未完成的行才不推进偏移，下一轮连同新内容一起重读。官方记录存在单行约 360KB 的超长行，早期实现因此在大文件上只吃掉首个读缓冲，已修复并有回归测试。
- `cache_steps(session_id, agent, step_uuid, model, time, ...)` 主键 `(session_id, agent, step_uuid)`；`cache_sessions` 存会话元数据；`cache_live` 存三项**状态**信号：未返回的后台子代理数、主代理是否处于阻塞等待、最近一次用户输入时间。这三项无法从已完成步骤推导，所以单列。

### 字段语义（真实样本确认）

- 模型只能从 `usage.record` 关联：`step.end` 不含 model，按 usage 四字段完全相等配对，且只接受 `usageScope == "turn"`（`session` 是累计值）。配对失败记 `UNKNOWN`；usage 为空的 `error`/`interrupted` 步骤不入库。
- 解码时长取 `llmServerDecodeMs`。实测 `llmStreamDurationMs == llmServerDecodeMs + llmClientConsumeMs`（160690/160692），而 `llmFirstTokenLatencyMs` 是从请求发出起算的累计延迟，在 109239/160692 条样本中大于 `llmStreamDurationMs`，相减会为负，因此不使用 `stream - firstToken`。
- 命中率 = `input_cache_read / (input_other + input_cache_read + input_cache_creation)`。
- **未上报判定**：某模型在统计窗口内 `input_cache_read` 与 `input_cache_creation` 双零时返回 `NOT_REPORTED`，界面显示"供应商未上报缓存数据"，不显示 0%。有非零缓存字段的模型才计算命中率（可以是真实的低百分比）。

### 动作

| 动作 | 权限 | 内容 |
|---|---|---|
| `cache.summary` | `usage:read` | 近 1 小时与今日总计、按模型分组、平均速度、`quota` 子对象（未知保持 UNKNOWN） |
| `cache.sessions` | `usage:read` | 活跃会话：标题、模型、距上次请求、最近 5 步命中率、上下文 token 估计、后台子代理数、是否阻塞 |
| `cache.history` | `usage:read` | `range = 24h / 7d / 30d`，按小时或按天聚合 |
| `cache.retention` | `usage:read` | 保留综合 `idleBuckets`，新增 `byModel`。分模型仅统计同一会话连续使用同一已知模型的主代理请求，按请求间隔返回 Token 缓存读取比例和样本数 |
| `cache.receipts` | `usage:read` | 保活/压缩/恢复的执行回执，区分"官方已接收"与"官方已完成" |

`monitor.summary` 已随 Monitor 插件化删除：侧栏组件是预装插件 `plugins/monitor/`，只经 `metrics:read` 读上面的 `cache.*`，核心不再保留这条私有通道。core 不新增常驻缓存指示（D10）。

### 缓存策略

- **后台等待保活**（默认关闭）：条件是会话运行中、存在未返回的后台子代理、主代理自己的最近一次模型请求距今达到阈值（默认 25 分钟，范围 5–60），且没有待审批、无 Goal 阻塞。时钟取 `cache_steps` 中 `agent='main'` 的最后一步，**子代理活动不刷新该时钟**。不受空闲保活次数限制。
- 后台等待保活文案固定为 `[保活]无需操作，继续等待`，通过 `prompts:steer` 插入当前回合，不使用“继续”这类会改变主代理行为的指令。
- **空闲会话保活**（默认关闭，默认 2 次，可按会话在右侧「会话信息 → 缓存保活」单行入口的弹出菜单中单独覆盖为跟随默认、关闭、保活 N 次或保活到指定时刻）：
  - 缓存时长与保活间隔按供应商类型自动推导：订阅模型（官方托管或 `api.kimi.com/coding`）默认 30 分钟缓存 / 每 25 分钟保活；开放平台（`api.moonshot.cn`、`api.moonshot.ai`）默认 5 分钟 / 每 4 分钟；其他供应商未知，默认不自动保活，除非在高级设置填写缓存时长。
  - 发送独立保活回合 `[保活]仅回复1`，不切换模型或思考参数；回合结束后核对仍是最后一轮且无文件改动，再调用官方 `POST /sessions/{id}:undo` 撤回该轮，使会话历史与后续请求前缀保持不变；若期间出现新回合则不撤回。
  - 护栏：运行中、待审批、阻塞等待、进行中 Goal、已归档/已完成、账号切换期间暂停、额度低于阈值（默认 10%）停止并经官方 toast（`kimi.warnings`）与单行状态提示；客户端关闭期间不补发。
  - 已抑制官方「回合完成」系统通知；会触发 `config.toml` 中配置的提交与结束钩子，且官方服务端仍可能刷新会话的最近更新时间（已在设置文案中披露）。
- **闭环**：发送后异步轮询主代理下一步——`input_cache_read > 0` 记成功；`= 0` 且该模型从未上报缓存字段记 `unknown`（不伪造成功）；`= 0` 但有上报则记无效；5 分钟内没有新步骤也记无效。**不自动重试。**
- **阻塞等待不可保活**：主代理停在阻塞调用里时消息只排队到等待结束。检测到该状态记 `skipped` 并说明原因，不发送、不记成功。
- **空闲自动压缩**（默认关闭）与**额度恢复**（默认关闭）保留；状态机按单会话互斥推进——等待子代理与空闲保活期间绝不压缩，空闲保活结束后仍然空闲且开启压缩时再进入压缩。压缩仍遵守“ACK 只记已接收，官方完成事件才记完成”。
- 各项策略分别授权。

### 设置页

`web/src/components/CacheSettings.tsx` 分五组：概览、会话、策略、实际用量、最近执行记录。只用宿主 CSS 变量与原生结构（`ekds-*` / `eks-*`），不铺不透明背景，不展示推测 TTL（D4）。表单规则：开关即时保存；数字失焦且校验通过才提交；后台 `automation` 事件不得覆盖正在编辑的草稿或写入中的策略。
