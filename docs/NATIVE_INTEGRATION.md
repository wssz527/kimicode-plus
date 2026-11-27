# 原生接入门槛与当前支持边界

Go Core 仍可独立运行。当前原生路径已在隔离 Kimi Code 1.4.2 / Electron 45.1.0（macOS ARM64 与 Windows x64）中完成契约检测式 CDP 附着、主进程代持核心凭据、正式 loopback RPC、KAP（官方本地会话 API）的会话读取/发送/事件、规划流式回复、网页内容提取、真实 Code 交接、多会话和内嵌设置；1.3.5 与 1.3.7 保留为更早的验证记录。它不是官方 SDK；注入不再按官方版本号白名单放行，而是按安全门槛和逐项界面/接口契约放行，单项失配时只停用对应功能。

**政策更新（2026-11-19，用户决定）**：本软件定位为本地个人开源工具。经用户显式授权，可改写官方客户端的用户数据、配置和凭据（改写前备份、原子写入、失败回滚），但仍不改官方程序包体。接入优先走官方公开面与本地 API；界面与插件经显式授权使用基于契约探针的运行时接入，单项失配即降级停用对应入口。

当前开发主实例未被附着、注入、重启或修改。验收使用独立 profile、独立 KIMI_CODE_HOME、独立 EKD 数据目录、独立测试工作区和本机合成模型/网页服务。三项自动化默认关闭；真实账号上的保活、压缩和额度恢复仍未验收。

## 同窗页面与宿主入口

官方界面以 Vue #app 为根，使用 data-color-scheme 与字体变量。`internal/desktop/assets/renderer.js` 向插件接口 v2 提供右侧 `pt-body` 面板、工具菜单入口及侧栏锚点。规划插件默认关闭，启用后注册一个“规划”入口，打开时才创建动态标签。内容使用 Shadow DOM 隔离 CSS，并跟随宿主主题。设置只嵌入官方设置对话框，不在侧栏克隆入口。旧多屏会话仍可按附加功能开关挂载到 `.chat-main`，其插件版 A2 已延期。旧内置规划挂载和演示工作台已移除。

入口注入按容器判重：`syncNativeEntries` 遍历每个可见的宿主菜单容器（空面板 `.sit`、`+` 下拉 `.pl`），在该容器内查同动作入口，多于一个则删除多余项，锚点行被重建则重新插入。判断依据是容器内是否已有入口，不使用全局单引用加 `isConnected`，因此官方重渲染任一容器后入口都能恢复且不会重复。

侧栏开合由 `layout.acquireSidebarCollapse()` 引用计数管理：第一次 acquire 时记录当前开合状态，最后一次 release 时按记录恢复；本来就折叠的侧栏不会被展开。规划和同屏各持一份 release，互不影响。

设置按官方层级嵌入官方设置对话框，不默认创建独立 BrowserWindow：多账号入口挂载到官方「账户」页顶部（凭证由系统钥匙串托管，启动后免密自动解锁），权限规则、子代理模型池、子代理时限和单轮步数上限挂载到官方「智能体与会话」页对应官方小节下方（并与官方 `secondary_model` 单选双向同步），会话并排与规划实验开关挂载到官方「实验室」页，模型能力留在官方「模型供应商」页，本地增强插件挂到官方「插件」页新增的 KimiCode+ 分段，缓存策略独立为「缓存与用量」标签页；「KimiCode+」标签页保留状态与诊断、功能归位索引，不注入官方插件市场分区。切换官方标签页或关闭弹层时自动卸载注入节点，官方某页契约探针失效时仅停用该处挂载并将受影响内容临时回落显示在 KimiCode+ 标签页里。侧栏 Monitor 是预装增强插件，挂载点由插件运行时自己的锚点管理，宿主重建时随插件停用一起释放，不留订阅和定时器。

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

EKD 由 `internal/desktop/inject.go`、`cdp.go`、`contracts.go`、`inspector_fuse.go` 和内嵌 JS 实现该机制。接入不再依赖官方版本号白名单：只要安全门槛（唯一主进程身份、Electron fuse 允许 inspector、渲染协议 `app://renderer/`）通过即允许附着。`internal/desktop/cdp.go` 的 `SupportedHosts` 目前只把 `1.3.4`/`1.3.5`/`1.3.7` 标为已人工验证；1.4.2 已在 macOS ARM64 与 Windows x64 完成实机接入与 KAP 验收（见 `docs/VALIDATION.md`），但该验证表尚未收录，因此在 1.4.2 上「状态与诊断」仍会显示"该版本未经人工验证，契约检测通过"，而不是已验证。把新版本加入该表属于代码改动，需与对应实机证据一起提交。各界面注入点与官方本地接口（`/config`、`/providers`、`/models`、`/oauth/usage`、`/sessions`）逐项探测，任一项失效仅停用并清理对应功能，其余功能照常运行。

**不再要求 `--user-data-dir`**。这一条曾把接入限制成"只能接隔离副本"，而从程序坞正常启动的实例没有该参数。现在的规则：

- `--host-profile` 为空时，「接入」（`attach --kimi-home <home>`）与自动发现都接受当前唯一主进程，并把它识别为**日常实例**。
- 日常实例必须由用户在设置里显式同意（`--allow-daily-instance` / 设置页的同意勾选）。没有这份同意时返回"需要授权"，绝不静默附着。
- 带 `--user-data-dir`、`--remote-debugging`、`--inspect` 的实例一律视为调试实例，不自动接入。

### Windows 运行中接入与兼容重开

开启「随 Kimi Code 启动自动接入」后，Windows 登录任务以当前用户的交互会话和普通权限启动增强核心。登录任务借助 Windows Script Host 隐藏核心控制台，直接运行 CLI 时保留标准输出。核心用 Win32 进程快照检测同一桌面会话中的候选 PID 和创建时间，候选变化时才执行完整校验。运行中的官方客户端可使用 Electron 自带的 Node 调试接口开启临时 inspector。接入前检查客户端路径、进程属主、启动时间、桌面会话及 Electron fuse，随后校验环回监听属主和 inspector 目标。安装与 macOS 共用的主进程桥后关闭临时端口。开始菜单、桌面和任务栏继续使用官方入口。旧版已改写的快捷方式在迁移时从备份恢复。

通过 `enhanced-kimi launch` 打开客户端时，增强仍可使用经校验的 Chromium 端口接入渲染进程。该兼容模式保留右侧面板等能力。独立网页、插件窗口和右侧「+」浮层需要主进程桥，增强插件页会提示从 KimiCode+ 入口重新打开并提供「立即重新打开」。重开动作通过客户端实例记录与监听属主核对会话服务，发现活动回合、待处理交互或无法确认状态时保留当前客户端。空闲时，兼容模式通过 Electron 请求正常退出，普通启动的实例通过 Windows Restart Manager 请求正常退出。确认旧进程退出后才启动。完全未接入时，可在独立工作台「设置 → Kimi Code 接入」查看状态和重开。

### macOS 登录常驻与事件驱动接入

同一开关安装 `com.user.enhanced-kimi.core-managed` 和 `com.user.enhanced-kimi.attach`。开启时将核心复制到增强数据目录的 `resident/enhanced-kimi`。两个产品登录项和自更新使用该副本。核心项在登录时启动，并由 launchd 保持运行。若同一数据目录已有核心，常驻进程等待现有实例释放锁后再接管。接入项在加载时运行，并监听 `KIMI_CODE_HOME/server/instances`、`server.token` 和增强数据目录的 `runtime.json`，覆盖客户端重启和核心更换端口或凭据。同一个宿主和核心之间，实例心跳唤醒会复用已有接入。

两个 plist 都使用 `0600` 和原子文件替换。接入项从对应数据目录读取并验证当前的环回地址和配对凭据。`attach.log`、`resident-core.log` 和 `attach-state.json` 用于查看启动和接入结果。

迁移时先在「状态与诊断」开启产品的自动接入。若界面显示旧版接入已开启，先关闭再开启。手工创建的 `com.user.enhanced-kimi.core` 由原配置维护，产品仅检测其存在。需要交接时，请在开启产品常驻后停用旧手工任务，再确认产品核心已启动并接入。针对日常实例开启或关闭自动接入时，产品会自动在 `~/Applications/KimiCode+.app` 维护轻量启动器供聚焦搜索和访达手动重开；从运行中的日常常驻核心关闭开关时，会先通过 LaunchServices 启动脱离 `launchd` 的接管核心，再移除两个登录项，避免直接中断当前已接入的会话。

### Electron fuse 位次与版本绑定

- Node inspector 信号 fuse 的**位次是 Electron 源码顺序的属性，不是从二进制里推得出来的**，跨大版本可能移动。因此位次按 Electron 版本显式绑定在 `VerifiedFuseLayouts` 表中（当前仅 `45.1.0`：block version 1、9 个 fuse、信号位次 3），并与宿主契约共用同一个 `SupportedElectron` 常量。
- 表中没有的 Electron 版本**失败关闭**，返回"该 Electron 版本未经验证 fuse 布局"，不猜测新布局。声明数量、block version 与位次表不一致时同样判为未知布局，而不是照旧读取可能已指向别的 fuse 的位置。
- 读取不再整文件加载（本机 `Electron Framework` 约 207MB，而 `attach.status` 会被设置页反复轮询）：改为 1MB 分块流式扫描并保留跨块 carry，内存与文件大小无关；同一文件（size + mtime）与同一 Electron 版本的结果缓存，文件变化即失效。
- 每次接入默认启用幂等跳过（`SkipCurrent`）：取到 `host-attach.lock` 后先复核目标宿主进程存活与身份未变（macOS 同时复核 fuse，Windows 兼容接入同时复核调试监听端口归属），再比对 `attach-state.json` 中同一存活核心与同一宿主的有效 `installed` 状态及 `attachKey`（严格核对 `corePid`，`runtime.json` 损坏返回不匹配、缺失时回退比对当前进程 PID，并通过进程存活检查及环回 `/enhance/v1/health` 的 `Bearer` 鉴权确认核心确实存活；CLI 解析 `runtime.json` 时同样始终经 `/enhance/v1/health` 实探通过才采纳）；全部匹配才跳过重复注入并返回既有状态，需要强制重新附着时可显式传入 `--force`。未命中跳过而即将触碰宿主调试端口或拆桥前（Windows 运行中接入会先确认本地临时调试端口空闲），持锁先把磁盘上已有的 `installed`、损坏或因非 `ErrNotExist` 错误不可读的 `attach-state.json` 原子替换为 `status: "attaching"`（替换失败则尝试删除；若替换与删除均失败，则无条件前置中止接入并返回持久化失败，原桥保持完好不受触碰）。Windows 运行中主进程接入失败并回退兼容 CDP 路径时全程共用单次 `host-attach.lock` 事务，中途不释放锁也不提前写盘，直到主进程尝试与必要的兼容回退全部结束后才在持锁期间完成唯一一次状态提交；若主进程尝试在拆桥前失败且回退到兼容路径时 `SkipCurrent` 命中接入前的有效兼容 `installed` 状态，则直接恢复该 `installed` 状态并跳过重复 CDP 注入（已拆桥时则强制重注）。凡经过 `host-attach.lock` 的接入结果均在持锁期间提交状态并标记已提交，外层 CLI 与后台流程不再在锁外二次覆盖；若持锁写盘失败，绝不吞掉错误或伪标 `stateRecorded`，而是标记为持久化失败向上传递（覆盖写入失败时同时尽力移除可能过期的旧 `attach-state.json`，移除亦失败时一并 `errors.Join` 进错误返回），外层亦不再在锁外重复覆盖。未取锁前提前失败的兜底写入先非阻塞尝试 `host-attach.lock`，若锁正被另一活跃接入持有则交由持锁方写出权威状态。未触碰现有桥的瞬时探测或锁竞争错误会保留健康的 `installed` 状态；一旦触碰或拆除旧桥后失败，或宿主/核心换代、退出，则如实更新为 `error`（并清除 `attachKey`）以便后续重新完成注入。需要授权时该文件带 `consentRequired: true`，设置页据此给出说明而不是只显示一个失败状态。

命令行入口：

```
enhanced-kimi attach --data-dir <dir> --kimi-home <home> [--host-profile <path>] [--allow-daily-instance] [--force]
enhanced-kimi attach --data-dir <dir> --kimi-home <home> --install [--host-profile <path>] [--allow-daily-instance]
enhanced-kimi attach --data-dir <dir> --uninstall
enhanced-kimi attach --status --data-dir <dir>
```

早期 1.3.3 独立 BrowserWindow 与 1.3.4 设置内嵌样例是历史验证。自 1.3.5 起使用正式本地 RPC。真实 KAP 由 Go 客户端读取指定 KIMI_CODE_HOME 下的 `server/instances/` 实例记录和 `server.token`，访问 `/api/v1`。实例记录和凭据在连接失败或 401 时重新解析一次再重试，非幂等请求不会被盲目重发。

同屏会话展开选择器时读取官方会话列表，按 `page_token` 分页。可选列表短暂为空时，选择器会重新读取。同屏新建的会话会先加入列表，等官方列表收录后再以官方数据为准。会话映射支持顶层 `cwd`、字符串 `activity`，也兼容旧版的 `metadata.cwd` 和 `activity.status`。每个窗格打开时读取历史并订阅会话事件，关闭或切换时释放该窗格持有的订阅。

## 网页、自动化与 Monitor

规划插件通过 `browser:view` 创建隔离 WebContentsView，视图按插件、renderer 实例和 viewId 归属。打开、导航和核心登记清理按视图串行处理。关闭时先中断由当前令牌、实例和窗口拥有的加载视图，避免网页响应阻塞释放。加载结束后核对宿主仍存活，失败时回滚核心登记。支持五站消息适配器、选区/正文回退、编辑预览和向真实 Code 会话交接。预览绑定规范化后的内容、目标和新会话字段，字段变化或旧响应均不能沿用确认。反向支持把摘要或 Diff 填入网页输入框，不自动发送。真实站点登录后的解析和回填仍未验收。来源切换保留草稿，菜单和弹层出现时隐藏网页子视图，实际可见性和坐标分别验证。

原生多会话主窗格跟随官方侧栏，同一添加入口逐次追加至三个，已显示会话通过交换避免重复。输入草稿、模型、模式、权限和附件按会话集中保存。系统文件选择返回路径，经 Go 校验最多 16 个本机常规文件后复用 KAP prompt 发送；官方消息保留时间和附件元数据。合成文件真实发送与读回已通过，系统文件对话框仅使用确定性返回值验证调用，未 GUI 点选；这不等于网站上传或旧 Outbox 上传。

`automation_scheduler.go` 每五秒检查状态。缓存保活、压缩与额度恢复默认关闭，安全模式下暂停执行。缓存保活遵守全局开关，关闭时单会话配置不会启动扫描。长工具回合与后台等待只参与空闲判断。压缩要求官方 `context_tokens/context_limit` 明确，`:compact` ACK 与 `compaction.completed` 分开记录。原生会话恢复要求最后一轮明确 429、无新用户接管且 Goal 硬预算允许，并核对官方额度窗口。规划线程的受控恢复复用原 revision。核心重启后，未核实的保活等待下一次真实请求，其他未确认的自动化意图记为 UNKNOWN。

Monitor 位于会话侧栏，默认约 233px 的紧凑布局，详情折叠。界面参考 `bfjnbvf/kimi-code-monitor@1d529f0b424a708971cfac097238df444f3b5beb`，MIT 归属与全文随构建交付，不启动上游采集器。额度按官方 `/oauth/usage` 的 kind 和 quota 结构区分 AVAILABLE、ERROR 与 UNKNOWN，只有有效数据才显示 5h/本周已用比例；读取错误不保留旧比例。输入、输出、速度和缓存命中没有样本时显示 `--`，诊断保留 UNKNOWN。真实自动化回执单独放在详情中，不冒充缓存命中率。

## 可执行的合成配对桥

`internal/desktop/pairing.go` 的 NewIsolatedTestHost / PairIsolatedHost 是独立 httptest 协议，仅报告只读合成能力。身份、版本、challenge、scope、Host/Origin、有效期及未授权拒绝已有回归。它不是官方 SDK 或生产配对入口，也不会启动第二个官方实例。

## 最小隔离实机验证

原生验收使用与当前开发实例隔离、可丢弃的测试宿主、独立 profile、独立 KIMI_CODE_HOME 和独立 EKD 数据目录。A3 当前脚本为项目内 `work/a3-stage/verify-a3.mjs`，由 `A3_STAGE_DIR` 指定独立运行目录，逐阶段写入报告并绑定源码及二进制 SHA-256。当前候选结果见 `work/a3-stage-final/a3-acceptance-report.json`。

以下 `work/ekd-final-integration/` 脚本是插件化前的历史验收记录，不能作为旧规划入口仍存在或 A2 插件已完成的依据。

1. `verify-review-fixes.mjs` 是当时的 UI 验收入口，检查动态容器隔离、空会话入口、统一规划默认网页、两窗格追加为三窗格、官方侧栏切换/交换、草稿保留、深浅色和窄宽度；还覆盖当时的 5 项设置列表与独立管理窗口跳转、标签顺序/恢复、缓存撤销/默认/保存读回、紧凑 Monitor。它是设置归位之前的记录，不代表现行标签层级。旧 `verify-native-ui.mjs` 和 `verify-native-flow.mjs` 仅作早期记录。
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
| 凭据 | 系统钥匙串托管密钥（macOS Keychain / Windows DPAPI）的 AES-GCM 库、启动免密自动解锁、旧主口令库兼容、secretRef；多账号切换经备份后原子更新官方凭证文件并失败回滚，旧的显式只读导入接口不回写 | refresh_token 刷新；其他平台无钥匙串后端时不回写官方存储 |
| 内置浏览器 | 自建 WebContentsView/窗口、选区/正文提取、编辑并交接 Code，视图可见性与坐标已核对 | 网站内对话、上传、登录态恢复与完整窗口合成截图 |
| 压缩与保活 | 原生后台调度、真实 KAP 请求、意图/回执、Goal/审批/竞争保护；合成回归通过 | 真实账号上的自动动作验收 |
| 多会话/Tower | 最多三个 KAP 会话，逐次追加、主窗格跟随与交换去重；独立草稿/模型/模式/权限/附件/停止/审批；本地 Worktree 和布局偏好 | Reviewer 仍只是本地偏好 |
| KCT/Monitor | Go 指令整理、兼容事件导入；紧凑侧栏、官方额度状态、MIT 界面归属与许可，回执与命中率分开 | 完整真实速度/缓存观测、真实账号额度验证、外部插件市场 |
| 设置 | 官方原生标签归位：账户页多账号、模型供应商页能力抽屉、智能体与会话页权限与子代理池、实验室页实验开关、插件页 KimiCode+ 分段、独立「缓存与用量」页、「KimiCode+」诊断页与探针回落；原生配置保存后经 GET 读回，缓存保存读回，只读诊断（接入日志尾部与开源许可） | 更多真实账号和平台组合；非 macOS/Windows 平台的钥匙串后端 |
| 更新与插件热更新 | 核心按 CNB 优先、GitHub 兜底读取 `manifest.json` + `SHA256SUMS` 并校验后自替换；官方插件从 `plugins-latest` 通道验 Ed25519 签名后热替换，扩权需二次确认 | 真实线上插件新版热替换的长周期观察 |
| 发布 | 三平台纯 Go 交叉构建（macOS ARM64、macOS Intel、Windows x64）、macOS ARM64 与 Windows x64 运行 | macOS Intel 原生运行、签名公证、Windows ACL |

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
| `cache.receipts` | `usage:read` | 保活回执区分 `sent`、`hit`、`miss` 与 `unknown`，压缩回执区分官方接收与完成 |

`monitor.summary` 已随 Monitor 插件化删除：侧栏组件是预装插件 `plugins/monitor/`，只经 `metrics:read` 读上面的 `cache.*`，核心不再保留这条私有通道。core 不新增常驻缓存指示（D10）。

### 缓存策略

- 开启全局「缓存保活」后，空闲会话默认每轮最多发两次。会话信息可选择跟随全局、关闭或指定次数。已冷却的历史会话等待下一次真实主代理模型请求。旧版指定截止时刻的会话迁移为关闭，并提示重新选择次数。
- 间隔使用同模型 `cache.retention` 分桶。每个可用分桶需要至少 5 个样本，缓存读取比例达到 70% 时采用该桶下界，并留出安全余量。同一已知模型的分桶有输入用量且缓存读取量为零时，按零读取处理。最短分桶低于阈值时暂停。样本不足时，订阅模型默认每 25 分钟尝试一次，开放平台和其他供应商默认每 4 分钟尝试一次。界面标出间隔来源。
- 各供应商都使用每轮 40 万等价 Token 的发送预算。发出前用最近输入或当前上下文加上探测余量估算，若超过剩余预算便停止发送。官方接口无法限制单次请求的实际输出量，因此单次实际用量可能超过预留。结算使用该保活回合已观测到的模型步骤用量，保留发送前估算作为记账下限。缓存读取按完整输入 Token 计入。连续两次确认未命中会停止。订阅模型还有剩余额度阈值，默认为 10%。
- 只在空闲会话发送独立的短回合，沿用会话的模型与思考参数。用唯一提示标记、官方消息 ID 与回合 ID 核对归属，再从同一回合的主代理步骤读取用量。缓存字段不足时记为 `unknown`。可确认未送达的临时失败经过退避可再次尝试，发送结果未核实且超出观察窗口时等待下一次真实请求。
- 回合完成、工作区复核一致，再确认回合仍位于末尾且会话空闲后，才调用官方 `POST /sessions/{id}:undo`。发现新回合、工具调用、审批或工作区变化时保留回合。保活可能触发官方通知和会话钩子。停止或熔断通过 `keepalive.alert` 在增强应用内提示。
- 运行中、等待后台任务、审批和进行中的 Goal 时暂停保活。会话仍有可继续的热前缀或保活回合尚在核验时，空闲压缩等待。官方明确报空闲时忽略本地残留的后台任务计数。
### 设置页

`web/src/components/CacheSettings.tsx` 分五组：概览、会话、策略、实际用量、最近执行记录。只用宿主 CSS 变量与原生结构（`ekds-*` / `eks-*`），不铺不透明背景，不展示推测 TTL（D4）。表单规则：开关即时保存；数字失焦且校验通过才提交；后台 `automation` 事件不得覆盖正在编辑的草稿或写入中的策略。
