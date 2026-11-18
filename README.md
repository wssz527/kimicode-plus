# Enhanced Kimi Desktop

独立的 Kimi Code Desktop 模块化增强软件。Go 运行业务、受保护的 Loopback HTTP 和纯 Go SQLite，React 静态界面通过 `go:embed` 内嵌发布。独立核心不需要 Node.js、Rust 或额外浏览器运行时；原生模式复用官方客户端已有的 Electron/Chromium，不另带一份。

产品核心为**缓存与额度**、**Kimi Code 配置可视化**和**插件系统**。Monitor、KCT、规划与定时任务作为预装插件运行；预装插件默认关闭，经插件页启停，`example-hello` 仅作为仓库内的开发模板保留、不再预装。多屏会话插件（A2）已延期，旧同屏功能默认关闭，保留作为后续迁移参考。计划见 [核心重整](docs/plans/2026-11-16-core-replan.md)、[官方功能插件](docs/plans/2026-11-16-addons-plugins.md)和[修正轮计划](docs/plans/2026-11-18-correction-round.md)。

核心重整 M0–M6、插件接口 v2（A1）、规划插件（A3）及 2026-11-18 修正轮（R0–R4）已进入主工作区。入口层级、供应商能力补齐、子代理模型池、Monitor 桌面版复刻与余额展示、定时任务调度、保活、空闲压缩与额度恢复均已有隔离 1.3.7 宿主验证和回执。当前工作树仍不是正式发布版；真实付费模型、真实账号自动恢复、五站登录后的操作和其他平台原生运行尚未验收。最新证据与限制见 [docs/VALIDATION.md](docs/VALIDATION.md)。

## 当前能力

- **按需启用与原生入口**：Monitor、KCT、规划、定时任务均通过“插件”页启停，默认关闭。插件通过 `ui:entry` 注册的入口同时出现在官方“会话信息 → 可打开”分组、右侧空白面板列表与右侧面板 `+` 菜单浮层（`browser-overlay`），通过 `ui:sidebar-nav` 注册的导航行位于官方左侧栏“新建会话”下方；停用后所有入口、动态标签、主区域页面、网页视图、监听和侧栏占用均立即释放。
- **供应商模型能力补齐与子代理模型池**：不再维护独立的“模型与供应商”页面或 `/models`、`/providers` 路由；在官方设置“模型供应商”的模型行上注入“能力与限制”弹层，直接图形化编辑图片输入、视频输入、工具调用、思考模式三段（不支持 / 可开关 / 始终开启）、自适应思考、上下文长度、最大输出长度与推理强度，保存后经官方接口读回核对并保留原密钥。子代理模型池与规划/定时任务统一从官方已配置模型目录选择，不再手填模型键。
- **轻量聊天**：Go 直接实现 OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent。用户配置供应商与凭据后才可显式发送，每次最多最近 12 条独立消息。请求意图、状态、回复和原始用量持久化。超时或崩溃后 UNKNOWN 不重发；用户核对上游后可显式解除阻塞，审计状态仍保持 UNKNOWN。协议测试使用本地 HTTP 合成服务，未调用真实模型。
- **凭据库**：独立 SQLite 中的 AES-256-GCM 密文，PBKDF2-SHA256 解锁；重启锁定，API 不提供密钥明文读取。独立的“凭据库”页面已移除，插件所需凭据在各自插件详情页中添加、替换和删除，界面只显示“已设置 / 未设置”，不回显明文。默认不访问系统钥匙串；用户显式指定官方 JSON 文件后可只读预览并导入为默认停用的账号，不回写官方文件，也不刷新或保存 refresh_token。
- **账号与本地网关**：供应商账号支持 revision、启停、优先级和加密 secretRef。`/v1/chat/completions` 提供独立本机令牌鉴权的 OpenAI 兼容流式/非流式调用，透传 reasoning_content、Kimi thinking 字段与工具调用回环，并将账号和 Token 用量入账。已用官方 Kimi CLI 2.4.1 在隔离 Home 中通过合成上游完成文本与真实 Read 工具回合；未调用真实付费模型。多账号调度、故障转移和图片/文件输入仍未实现。
- **用量中心**：输入/输出/缓存 Token、请求尝试、会话/项目/Agent 归类，PARTIAL/FINAL/CORRECTION、去重、用户 JSON 导入及轻量聊天响应入账。估算、实际账单和订阅/网关额度分开。未连接官方计费事件。
- **原生宿主接入**：支持经版本与 DOM 契约校验的 Kimi Code 1.3.4 / 1.3.5 / 1.3.7（Electron 45.1.0）。验收使用独立的 profile 和 KIMI_CODE_HOME。日常实例接入需单独同意。主进程代持核心会话，renderer 通过带独立凭据和来源校验的 loopback RPC 调用正式动作；旧 console-message 演示桥不再是当前路径。
- **规划与真实交接**：统一“规划”入口，默认网页来源，工具栏切换本地/网页。Go 持久化本地规划线程，支持流式回复、停止、预设、上下文/Skill/工具边界和写操作审批；输入区底栏选择规划模型与预设，历史位于更多菜单。可多选消息、编辑交接预览，并真实发送到新建或已有 Code 会话；未显式选择模型时不改变目标会话模型。隔离验收使用本机合成供应商，不代表真实付费模型已验收。
- **网页规划**：插件使用隔离 WebContentsView，首次打开 Kimi，可切换 DeepSeek、Gemini、Claude、ChatGPT。五站提供消息解析适配器，无法识别时退回选区或正文并提示。提取内容经编辑和确认后可交接给 Code，也可将会话摘要或 Diff 填入网页输入框，由用户自行发送。菜单或弹层出现时隐藏网页视图，切换来源保留草稿。已验证本地网页提取与交接；真实五站登录后的解析和回填尚未验收。
- **按需多会话（附加功能，默认关闭）**：同一个“添加同屏会话”入口逐次追加，总计最多三个。主窗格跟随官方侧栏会话，选中已显示的会话时交换位置而不重复；草稿、发送模式、模型、权限与待发送附件按会话保存。支持 Markdown、消息时间、复制和引用回复；圆形发送按钮、附件加号和输入区控件跟随原生视觉。三窗格流式更新、独立停止、Read 审批和本机文件经 KAP 发送已通过隔离验证。系统文件选择窗口的实际 GUI 点选尚未验收。
- **原生配置与设置聚合**：可读取和修改隔离宿主的工具、子代理模型池/时限、单轮步数与权限规则。KimiCode+ 只在官方设置对话框内出现，位于“已归档”前，首页为单层五项分组（缓存与额度、Kimi Code 配置、插件、附加功能、状态与诊断），子页提供返回，不再保留跳转到独立管理窗口的行；侧栏底部不再克隆重复的设置按钮。缓存撤销/默认/保存读回、浏览器权限及布局保存/恢复均已通过实机流程。RPC 请求 ID 在 `crypto.randomUUID()` 不可用时使用随机回退。
- **任务交接（旧通用 Outbox）**：选定消息、来源、Diff、附件持久化、脱敏预览、Outbox、检查点和幂等核对仍使用 Mock。该旧路径与上述真实 KAP `handoff` 是两条不同链路，不能把 Mock 回执当成官方送达。
- **会话与执行策略**：本地会话引用、revision、布局和 Reviewer 偏好持久化；指定仓库创建 detached Worktree。原生布局 UI 只提供 1–3 列，旧布局 API 为兼容已有数据仍接受 1–4 列。旧策略模块只评估空闲条件。原生五秒调度器实现保活、空闲压缩与额度恢复，三项分别授权且默认关闭；安全模式不执行自动化，未知回执不重发，压缩 ACK 不等于完成。已用合成 KAP 覆盖意图、竞争、Goal 阻断、额度窗口、重启 UNKNOWN 与撤销监听，真实账号上的自动动作尚未验收。
- **Monitor 与余额观测**：按 `bfjnbvf/kimi-code-monitor@1d529f0b` 桌面客户端版完整复刻侧栏模块化面板、Rive 蓝球形象（空闲呼吸、思考/调用计时、彩带与点击动作）、长按三区（隐藏 / 展开 / 固定）拖拽排序与迷你模式；数据全部来自核心 `metrics:read`，合并展示 Kimi Code 5h/每周额度条、加油包余额以及官方已配置供应商的外部余额（DeepSeek、Moonshot、BigModel、Magpie 等），读取失败绝不显示为 0，API 密钥仅在 Go 端用于请求头，不进入渲染进程或日志。
- **定时任务调度**：核心持久化调度引擎（`internal/scheduler`）配合预装可选插件 `kimicode-plus.scheduler`，支持单次、预设、每日多时刻和自定义间隔/周几调度，支持错过运行合并补跑一次或跳过、新开会话与绑定会话投递、未读运行记录与四套只读分析模板。无人值守遇到审批请求时不自动批准，标记为“等待批准”提示人工处理。
- **浏览器数据**：用户主动提供导出 JSON，按指定网站或全部数据预览，处理目标 Cookie 冲突并保存到增强加密隔离库。网站操作权限独立保存。未读取 Chrome/Edge 数据库，未恢复到任何真实浏览器。
- **可移植插件**：可信内置 Go 能力门禁、生命周期与故障隔离，预装插件版本递增时启动自动平滑升级并保留授权与私有存储。Monitor 上游 MIT 归属、Rive 资源说明与 Kimi Work+ 余额模块来源随 `NOTICE` 和构建通知一并交付；不运行上游采集器、安装器或独立 Node 服务。

网页功能支持内容提取、向 Code 交接，以及只填入而不自动发送的网页回填。真实网站账号下的提取与回填未验证；网站内自动聊天和网站附件上传不在本轮范围。本机文件经 KAP 向 Code 发送已有独立验收。

## 原生界面当前状态

插件入口克隆官方“会话信息 → 可打开”、右侧空白面板列表与 `+` 菜单浮层的原生行结构，左侧栏导航克隆“新建会话”行，严格跟随插件启用状态并在运行期实时生效。规划和当前会话定时任务挂载到右侧 `pt-body`，只在打开时出现动态标签，关闭后移除；全局定时任务挂载在主聊天区域（`mode: "page"`），点击会话或“返回聊天”即恢复原生视图。设置在官方设置对话框内，首页为五项分组；Monitor 位于会话侧栏底部，透明融入宿主毛玻璃材质。Shadow DOM 隔离样式，主题跟随宿主变量。已移除独立模型页、独立凭据页、独立管理窗口跳转行、旧内置规划入口、演示工作台、常驻会话选择器和空闲状态带。

暗色、亮色、双/三窗格及窄宽度场景已检查，输入区不重叠；真实网页视图的位置与可见性已验收。主 WebContents 截图不包含子 WebContentsView，且当前 macOS 录屏权限未取得完整窗口合成截图，因此不把局部截图写成完整窗口视觉证据。

## 本机验证

双击项目根目录的 `启动增强演示.command` 会用当前 Go 二进制启动增强核心（缺失时先执行 `sh scripts/build.sh`），读取数据目录里的 `runtime.json` 并打开配对地址；它不再调用 `work/` 下任何历史 Node demo。默认数据目录为 `./work/development`，可用 `EKD_DATA_DIR`、`EKD_PORT` 覆盖。

要接入隔离的官方宿主，在上面的基础上追加宿主参数，不要用演示脚本的默认参数去连真实客户端：

```bash
./dist/enhanced-kimi \
  --data-dir /absolute/path/to/ekd-data \
  --host-profile /absolute/path/to/isolated-profile \
  --kimi-home /absolute/path/to/isolated-kimi-home \
  --workspace /absolute/path/to/workspace
```

官方宿主必须已经以对应 profile 和 KIMI_CODE_HOME 启动。所有路径都应属于可丢弃的隔离验收环境；不要指向正在使用的开发主实例或真实配置。

当前原生面板包含独立规划历史、三档预设与上下文/Skill/工具边界、网页选区提取、真实 Code 会话交接、最多三个同屏会话、设置聚合及可撤销的恢复监听。本机文件的附件条目、发送与官方消息读回已经验证，系统原生文件选择窗口未实际 GUI 点选。合成模型只产生本机测试回复，不产生真实模型费用；切换真实供应商后才会请求相应模型。真实网站内对话/上传、真实账号自动保活/压缩/恢复和其他平台原生运行均未验收。

## 构建与启动

开发需要 Go 1.27.3、Node.js 24、npm 和 Python 3。SQLite 使用 `modernc.org/sqlite`，默认 `CGO_ENABLED=0`。

```bash
sh scripts/build.sh
./dist/enhanced-kimi --data-dir ./work/development
```

服务只监听 `127.0.0.1` 的随机端口。打开输出所指 `runtime.json` 中的 `pairingUrl`，页面完成配对后移除地址 fragment。也可打开 `url`，输入 `pairingToken`。凭据只属于本增强服务，重启失效，勿分享运行文件。页面刷新需要重新配对。

```bash
./dist/enhanced-kimi --data-dir ./work/development --port 43125
./dist/enhanced-kimi --data-dir ./work/recovery --safe-mode
./dist/enhanced-kimi --version
```

Ctrl+C 关闭增强程序。同一数据目录使用操作系统锁保证单实例。默认目录为 `os.UserConfigDir()/EnhancedKimiDesktop`。独立环境使用不同 `--data-dir`，不要指向官方目录。macOS/Linux 使用 0700 目录和 0600 数据文件；Windows 依赖用户目录 ACL，尚未原生验证。

首次使用真实供应商前，在凭据库设置至少 12 字节口令，保存密钥引用，将同一引用填入供应商 `secretRef`。公共 models.dev 导入不需要密钥。真实付费调用并非本轮验收内容。

## 开发与验证命令

```bash
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
(cd web && npm ci && npm run typecheck && npm test && npm run build && npm run build:host)
python3 scripts/acceptance.py --binary ./dist/enhanced-kimi
python3 scripts/measure.py --binary ./dist/enhanced-kimi --output work/performance.json
sh scripts/crossbuild.sh
```

测试限定生产源码目录，避免把 `work/` 中历史审查用孤立探针当作产品包。验收脚本只启动独立增强服务，使用临时数据库和合成任务。协议集成测试只调用本地合成 HTTP 服务。交叉构建前先完成 `scripts/build.sh`，确保内嵌最新前端；五平台交叉编译不等于在五个平台运行通过。

开发模式：

```bash
go run ./cmd/enhanced-kimi --data-dir ./work/development --port 43125
(cd web && npm run dev)
```

Vite 开发页面为 `http://localhost:5173`，使用本增强服务的配对凭据。正式页面直接由 Go 提供。`?mode=diagnostic` 显示原有 Mock 流程、总览及故障工具，仅用于独立开发数据。

## 目录

| 路径 | 责任 |
|---|---|
| `cmd/enhanced-kimi` | 启动、数据目录锁、信号退出、运行凭据 |
| `internal/server`、`security`、`config` | 模块接线、访问控制、安全模式、设置 |
| `internal/storage` | SQLite、迁移、事务 |
| `internal/registry`、`usage` | 模型、版本变更、公共目录、独立用量账本 |
| `internal/provider`、`chat` | Go 供应商协议、账号与显式只读导入、独立聊天与请求状态 |
| `internal/gateway` | 本机令牌、模型路由、OpenAI/Kimi 流式与工具回环、网关用量归因 |
| `internal/credentials` | 口令解锁的加密凭据库 |
| `internal/bridge` | 旧版任务、附件、Outbox 与 Mock transport |
| `internal/desktop` | KAP 会话/事件、供应商模型能力读写、规划接入、原生运行时 RPC、自动化调度与回执、余额聚合服务 |
| `internal/quota` | Kimi Code 订阅与加油包额度、DeepSeek / Moonshot / BigModel / 本机 Magpie 余额读取与身份级回退 |
| `internal/scheduler` | 持久化定时任务调度引擎、错过补跑/跳过判定、单任务互斥与运行记录 |
| `internal/future` | 会话引用、Worktree、策略、浏览器隔离数据和权限 |
| `internal/plugins`、`plugins` | 可信 Go 插件能力门禁、生命周期、官方摘要校验与内嵌预装插件（Monitor、KCT、规划、定时任务） |
| `adapters` | 浏览器兼容 TypeScript 适配契约与夹具 |
| `web/src/host-entry.tsx` | 独立管理页与原生 surface 的可装配 ES 模块 |
| `internal/desktop/assets` | 宿主入口、面板/设置窗口、WebContentsView 与正式本地 RPC |
| `web`、`internal/webassets` | React 源码与 Go 内嵌资源 |
| `scripts`、`.github/workflows` | 构建、验收、测量与 CI |

[API 契约](docs/API_CONTRACT.md) · [架构](docs/ARCHITECTURE.md) · [验证结果与修复记录](docs/VALIDATION.md) · [原生接入门槛](docs/NATIVE_INTEGRATION.md)
