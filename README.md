# Enhanced Kimi Desktop

独立的 Kimi Code Desktop 模块化增强软件。Go 运行业务、受保护的 Loopback HTTP 和纯 Go SQLite，React 静态界面通过 `go:embed` 内嵌发布。独立核心不需要 Node.js、Rust 或额外浏览器运行时；原生模式复用官方客户端已有的 Electron/Chromium，不另带一份。

产品定位已按 2026-11-16 核心重整计划收口为三个核心加一个开放插件层：**缓存与额度**、**Kimi Code 配置可视化**、**插件系统**；Monitor 和 KCT 是按插件接口适配的预装插件。规划与多屏会话降级为附加功能，本轮冻结、默认隐藏，代码保留待后续改造成插件。计划见 [docs/plans/2026-11-16-core-replan.md](docs/plans/2026-11-16-core-replan.md)。

0.2.0 的原生功能聚合与界面返修已完成隔离验收：设置和 Monitor 使用官方窗口结构，保活、空闲压缩与额度恢复已有受控调度及回执。当前工作树仍不是正式发布版；真实付费模型、真实 managed 账号恢复、网站内对话和其他平台的原生运行尚未验收。

## 当前能力

- **附加功能开关**：规划与多屏会话默认关闭，状态存 SQLite `native_state` 的 `features` 行，可用 `features.get` / `features.set` 读写。关闭时增强层不注入规划入口、`添加同屏会话` 入口和右栏胶囊，已打开的面板会被卸载并恢复原生内容；显式开启后入口立即恢复。功能开关当前经 RPC 动作管理，设置界面归 M6 接入。
- **模型中心**：真实 models.dev 公共目录获取、缓存、显式供应商实例映射导入、逐字段来源、UNKNOWN、人工覆盖、版本冲突及事务回滚。供应商模型列表支持 OpenAI 兼容、Anthropic、Gemini；能力不通过推理探测。
- **轻量聊天**：Go 直接实现 OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent。用户配置供应商与凭据后才可显式发送，每次最多最近 12 条独立消息。请求意图、状态、回复和原始用量持久化。超时或崩溃后 UNKNOWN 不重发；用户核对上游后可显式解除阻塞，审计状态仍保持 UNKNOWN。协议测试使用本地 HTTP 合成服务，未调用真实模型。
- **凭据库**：独立 SQLite 中的 AES-256-GCM 密文，PBKDF2-SHA256 解锁；重启锁定，API 不提供密钥明文读取。默认不访问系统钥匙串；用户显式指定官方 JSON 文件后可只读预览并导入为默认停用的账号，不回写官方文件，也不刷新或保存 refresh_token。
- **账号与本地网关**：供应商账号支持 revision、启停、优先级和加密 secretRef。`/v1/chat/completions` 提供独立本机令牌鉴权的 OpenAI 兼容流式/非流式调用，透传 reasoning_content、Kimi thinking 字段与工具调用回环，并将账号和 Token 用量入账。已用官方 Kimi CLI 2.4.1 在隔离 Home 中通过合成上游完成文本与真实 Read 工具回合；未调用真实付费模型。多账号调度、故障转移和图片/文件输入仍未实现。
- **用量中心**：输入/输出/缓存 Token、请求尝试、会话/项目/Agent 归类，PARTIAL/FINAL/CORRECTION、去重、用户 JSON 导入及轻量聊天响应入账。估算、实际账单和订阅/网关额度分开。未连接官方计费事件。
- **原生宿主接入**：支持经版本与 DOM 契约校验的 Kimi Code 1.3.4 / 1.3.5（Electron 45.1.0）。仅在显式指定隔离 profile 与对应 KIMI_CODE_HOME 时附着，不修改官方程序包。主进程代持核心会话，renderer 通过带独立凭据和来源校验的 loopback RPC 调用正式动作；旧 console-message 演示桥不再是当前路径。
- **规划与真实交接**：统一“规划”入口，默认网页来源，本地/网页在菜单内切换。Go 持久化本地规划线程，支持流式回复、停止、预设、上下文/Skill/工具边界和写操作审批；模型、历史和预设放入配置菜单。可多选消息、编辑交接预览，并真实发送到新建或已有 Code 会话；未显式选择模型时不改变目标会话模型。隔离验收使用本机合成供应商，不代表真实付费模型已验收。
- **网页规划**：增强创建的 WebContentsView 或独立网页窗口可以读取用户选区，没有选区时读取 main/body 文本；内容可编辑并交接给 Code。菜单、会话选择和设置弹层出现时隐藏网页视图，避免覆盖原生控件；切换来源保留草稿。已核对真实视图的可见性与同步坐标；它不是通用网站聊天适配器，不会自动填写第三方网站输入框、发送网站消息或读取网站回复。
- **按需多会话（附加功能，默认关闭）**：同一个“添加同屏会话”入口逐次追加，总计最多三个。主窗格跟随官方侧栏会话，选中已显示的会话时交换位置而不重复；草稿、发送模式、模型、权限与待发送附件按会话保存。支持 Markdown、消息时间、复制和引用回复；圆形发送按钮、附件加号和输入区控件跟随原生视觉。三窗格流式更新、独立停止、Read 审批和本机文件经 KAP 发送已通过隔离验证。系统文件选择窗口的实际 GUI 点选尚未验收。
- **原生配置与设置聚合**：可读取和修改隔离宿主的工具、子代理模型池/时限、单轮步数与权限规则。KimiCode+ 只在官方设置对话框内出现，位于“已归档”前，首页为单层 11 项列表，子页提供返回，不再使用七分类和嵌套胶囊导航；侧栏底部不再克隆重复的设置按钮。缓存撤销/默认/保存读回、浏览器权限及布局保存/恢复均已通过实机流程。RPC 请求 ID 在 `crypto.randomUUID()` 不可用时使用随机回退。
- **任务交接（旧通用 Outbox）**：选定消息、来源、Diff、附件持久化、脱敏预览、Outbox、检查点和幂等核对仍使用 Mock。该旧路径与上述真实 KAP `handoff` 是两条不同链路，不能把 Mock 回执当成官方送达。
- **会话与执行策略**：本地会话引用、revision、布局和 Reviewer 偏好持久化；指定仓库创建 detached Worktree。原生布局 UI 只提供 1–3 列，旧布局 API 为兼容已有数据仍接受 1–4 列。旧策略模块只评估空闲条件。原生五秒调度器实现保活、空闲压缩与额度恢复，三项分别授权且默认关闭；安全模式不执行自动化，未知回执不重发，压缩 ACK 不等于完成。已用合成 KAP 覆盖意图、竞争、Goal 阻断、额度窗口、重启 UNKNOWN 与撤销监听，真实账号上的自动动作尚未验收。
- **Monitor**：会话侧栏底部使用紧凑布局，默认只显示额度、四项指标和详情入口。额度读取官方 `/oauth/usage`，区分 AVAILABLE/ERROR/UNKNOWN，只在有效数据下显示已用比例；错误不保留旧比例。输入、输出、速度和缓存命中缺少观测时显示 `--`，诊断保留 UNKNOWN。自动化回执单独放在详情中，绝不当作缓存命中率；25/29/30 分钟不是官方 TTL。
- **浏览器数据**：用户主动提供导出 JSON，按指定网站或全部数据预览，处理目标 Cookie 冲突并保存到增强加密隔离库。网站操作权限独立保存。未读取 Chrome/Edge 数据库，未恢复到任何真实浏览器。
- **可移植插件**：可信内置 Go 指令整理、脱敏预览和 Monitor 兼容事件导入，按需启用并隔离故障。Monitor 界面参考 `bfjnbvf/kimi-code-monitor@1d529f0b`，MIT 归属和许可随构建交付；不运行上游采集器、安装器或独立 Node 服务。

网页入口不再只是导航，但仅支持增强自建网页内容的提取与向 Code 交接；网站内自动聊天、网站附件上传和登录态恢复仍未实现。这与已验证的本机文件向 KAP 会话发送不是同一条链路。

## 原生界面当前状态

原生入口克隆官方侧栏或空会话工具菜单的对应行，沿用其图标、间距和主题。规划在右侧 `pt-body`，多会话在主聊天 `.chat-main`，设置在官方设置对话框内，Monitor 位于会话侧栏底部。附加功能关闭时这三处规划/同屏挂载点都不出现：侧栏与 `+` 菜单没有对应行，右栏标签栏也没有 `KimiCode+ 规划` 胶囊。容器隔离持续覆盖 Vue 重建的官方子节点，切回原生工具、设置标签或单会话时释放增强内容。Monitor 与设置入口不依赖右侧工具起始页，原生浏览器标签已打开时核心重连也能恢复。Shadow DOM 只隔离样式，主题同步宿主变量。已移除 demo 工作台标题、常驻会话选择器、空闲状态带和无关英文元数据；设置不再默认弹出独立 Mac 窗口。

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
| `internal/desktop` | KAP 会话/事件、规划接入、原生运行时 RPC、自动化调度与回执、合成配对桥 |
| `internal/future` | 会话引用、Worktree、策略、浏览器隔离数据和权限 |
| `internal/plugins` | 可信 Go 处理能力与生命周期 |
| `adapters` | 浏览器兼容 TypeScript 适配契约与夹具 |
| `web/src/host-entry.tsx` | 独立管理页与原生 surface 的可装配 ES 模块 |
| `internal/desktop/assets` | 宿主入口、面板/设置窗口、WebContentsView 与正式本地 RPC |
| `web`、`internal/webassets` | React 源码与 Go 内嵌资源 |
| `scripts`、`.github/workflows` | 构建、验收、测量与 CI |

[API 契约](docs/API_CONTRACT.md) · [架构](docs/ARCHITECTURE.md) · [验证结果与修复记录](docs/VALIDATION.md) · [原生接入门槛](docs/NATIVE_INTEGRATION.md)
