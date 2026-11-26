# 架构与数据流

第一阶段使用单一 Go 进程运行业务模块、受保护 Loopback HTTP 和 SQLite。前端静态资源通过 go:embed 随二进制发布。TS 适配层保留在单独目录中，不要求常驻 Node，不加载官方引擎。

核心不运行插件宿主进程：增强插件是本地受信任的 ES Module 包（清单 + 单文件入口），由核心登记、授权并从插件自己的目录下发源码，renderer 用 blob URL `import()` 执行。`apiVersion: 1` 提供指标、会话、配置、界面插槽与私有存储；`apiVersion: 2` 增加会话发送、主区整页/分栏、右侧面板、独立网页视图与命令等挂载点，高权限能力安装时需要二次确认。官方预装插件与第三方插件走同一套接口，没有私有通道。

## 从工作台到数据

浏览器从本程序自己的运行文件获得一次运行期随机配对凭据。配对接口签发有限有效期、按资源授权的内存会话。每次 API 请求携带 Bearer；同时校验精确 Host、同源 Origin、跨站标记、请求容量和速率。页面 CSP 禁止外部脚本、外连和被嵌入。API 不使用 Cookie 鉴权，配对 fragment 不进入 HTTP 请求或 Referer。

Core 路由将请求交给 registry、usage、bridge、plugins 或 chat。模块仅依赖 `database/sql` 与窄工具包。SQLite 开启 WAL、foreign_keys 和 busy_timeout，单连接串行化持久化，分析可用只读连接并发取得 WAL 快照。迁移记录按模块维护版本，DDL 和版本推进位于同一事务中，较新 schema 拒绝降级。单个业务模块初始化失败只使该模块 FAULTED，核心设置和其余模块仍可启动。

配置使用 revision 条件写入。安全模式标记与派发准入由同一把锁串行化，派发获准后释放该锁。Worktree 创建和浏览器迁移应用同样经过该门。开启安全模式时，新派发立即被拒绝，设置接口为已获准的调用最多等待五秒，已发送的请求可继续完成。安全模式阻止插件启用并清理其资源，仍可查询历史。模块启停配置在重启增强服务后生效。程序只对自己的数据目录加操作系统锁，不读写官方 Session 锁。

## 模型、用量与任务

模型标识由 provider instance 和实际模型 ID 共同确定。每个能力字段独立保存 value、source、fetchedAt；UNKNOWN 用 null 表达，false 和零不能代替未知。更高可信来源胜出，同来源使用解析后的时间比较。批量配置预览、应用和回滚均在本软件数据库中完成，不暗示官方配置具有跨进程事务。

用量保留实际观察和请求尝试。缓存归一化遵循协议，重试 attempt 分开记账，重复来源按规范请求键关联。实际 Token、估算费用、官方订阅额度和网关额度不互相冒充。状态快照不直接累计成请求费用。历史带来源和归类，缺失字段保持 UNKNOWN。

旧通用任务桥先形成版本化包，保留用户所选消息、来源、Diff、附件和父检查点，经过脱敏预览再确认。SQLite Outbox 在外部调用前原子占用稳定 intent，并保存 payload hash。只有匹配 task/session/digest 的有效回执才能推进检查点。空回执、超时、派发中崩溃均保持 UNKNOWN，核对接口不重发。该路径的 desktop.Adapter 仍为 Mock；原生规划交接使用下文独立的 KAP 路径，不创建第二官方引擎。

模型编辑通过原子 snapshot 绑定实体与 revision，no-op 不推进版本。自动候选、人工能力和人工显示名分别持久化；取消覆盖会恢复候选，manual UNKNOWN 则继续阻挡自动值。回滚比较实际 after-state，后续编辑冲突时整批拒绝。

公共目录由用户主动读取固定 models.dev HTTPS API，在核心完成 TLS/域名/地址/重定向/大小和超时限制。原始快照与 ETag、SHA、抓取时间存 SQLite。失败保留旧缓存，导入显式映射实例并同时校验目录 hash 与配置 revision。此链路没有推理请求。

用量以 canonical attempt 保存累计口径，并单独保存 PARTIAL/FINAL/CORRECTION 观测及决定。附件先写私有 SQLite blob，Outbox claim 同事务绑定引用。传输只获得该任务的受限字节读取器，独立 Code/Web transport 验证目标和逐附件回执。未引用 blob 24 小时后可清理，历史引用保留。安全模式许可门紧邻真正 transport 调用，配合目标绑定限制将来的外部写入。

## 原生宿主与真实会话

`desktop.AttachNative` 校验进程、应用/Electron 版本和 DOM 契约。macOS 在系统及用户的 Applications 目录识别宿主，隔离接入匹配指定 profile，日常实例接入要求用户授权。主进程保存核心配对会话，并向每个获准 WebContents 发放独立 bridge key。renderer 请求经 loopback RPC 到主进程，再分发到核心动作或受限的 Electron 浏览器操作；来源、归属、方法和路径均校验。核心 token 不进入 renderer，宿主升级或 DOM 失配时增强停用。Windows 兼容桥为原生动作和 HTTP 转发分别签发受限权限，按明确的动作、方法和路径校验，并独立限流。

`KAPClient` 从指定隔离 KIMI_CODE_HOME 读取实例记录与本地服务 token，通过官方 `/api/v1` 读取会话/配置、创建会话、发送 prompt、停止和回复审批。WebSocket 事件按 session scope 进入 EventBus；`turn.ended` 只更新结束状态，不覆盖已流式保存的 Items。消息保留官方时间和附件名称/类型。规划由 Go Planner 单独持久化并通过配置供应商生成，网页选区由增强自建的 WebContentsView/窗口读取，编辑预览后经 `handoff` 向已有或新建 Code 会话发送。没有显式选择时请求不带 model 或 permission_mode，避免暗改目标；原生附件通过 Electron 文件选择取得路径，Go 校验最多 16 个本机常规文件后复用 KAP prompt 的 image/video/file 部件，不经过旧 Outbox blob store。

多会话 surface 最多挂载三个窗格。同一入口逐次追加，官方侧栏驱动主窗格，已显示会话通过交换避免重复。`SessionInputProvider` 按 session ID 集中保存草稿、模型、模式、权限及附件，事件仍按会话订阅。它复用官方会话服务，但不是官方原生的多视图 API。规划位于官方右侧 `pt-body`，多会话位于 `.chat-main`，容器属性与 CSS 持续隐藏 Vue 重建的原生子节点，退出时释放。设置不再维护平行配置页：多账号挂载到官方「账户」页，权限规则与子代理池挂载到官方「智能体与会话」页，会话并排与规划实验开关注入官方「实验室」页，本地增强插件挂在官方「插件」页的 KimiCode+ 分段，缓存与策略独立为「缓存与用量」标签页，「KimiCode+」标签页只保留状态诊断与探针回落内容。模型能力与限制通过原生行内抽屉读写，凭据在申请它的插件详情页维护。Monitor/设置入口沿用官方侧栏结构，网页在菜单/设置弹层出现时隐藏，避免 WebContentsView 覆盖控件。

## 降级与后续边界

旧 DesktopAdapter 与 Code/Web Outbox 任务传输仍为 Mock。原生网页路径可以提取增强自建网页的选区/正文并交接给 Code，但不会向第三方网站填写或发送消息，也不读取网站回复。轻量聊天通过 Go provider.Client 直接使用供应商 HTTP 协议，未经过 Node 中转。发送前保存稳定 intent 与 INFLIGHT，收到有效文本后记录结果；超时、未知和重启中断不自动重发。最近 12 条独立历史构成请求上下文。原始用量随请求保存并转入账本，读回线程时可补写，不把缺失 Token 转为零。

凭据库使用 PBKDF2-SHA256 派生密钥和 AES-256-GCM，随机 nonce 与引用作为附加认证数据。密钥本身交给操作系统钥匙串保管：macOS 用 Keychain（`MACOS_KEYCHAIN`），Windows 用 DPAPI（`WINDOWS_DPAPI`），首次运行生成随机密钥并由系统保护，之后每次启动 `AutoUnlock` 直接解密，用户不再输入主口令。旧的主口令库继续可打开，但保持锁定状态并在状态里标记 `legacyPasswordVault`，用户输入一次口令后才解除；忘记口令时只能重置整个库。API 仅列引用，不提供明文导出。用户提供的 Cookie JSON 进入独立密文表，普通凭据接口不能列出、读取或删除其中内容；供应商密钥仍限制为 32 KiB，浏览器数据单项限制为 8 MiB。升级只迁移与既有迁移记录关联的内部引用并保留密文字节。预览 24 小时后不可应用，启动及浏览器请求会清理到期密文，迁移记录不含值。冲突按目标分区、域、路径、名称等识别，与来源浏览器 storeId 无关。该路径不读真实浏览器数据库，也不恢复真实登录态。

会话引用、布局、Reviewer 偏好与策略保存在 SQLite。原生空闲前缀保活状态机使用可核验的会话活动、实测 Retention 和预算上限选择空闲保活或压缩。审批、主回合和工具执行会阻止后台派发。保活仅在空闲时发送独立短回合。压缩等待可验证的完成信号，未完成意图在核心重启后记为 UNKNOWN。恢复要求最后一轮明确 429、官方额度可用且 Goal 预算允许。定时任务在官方发送回执与可核验的回合历史中确认本次执行，并据此记录结果。未获得归属证据时会持续等待至超时或取消；若忙碌后回到空闲仍无可归属回合，则明确记为执行失败并提示打开会话核对。Worktree 功能仅在用户指定的 Git 仓库下创建 detached 工作树。

增强插件运行在核心登记的本地包内，不属于核心的可信 Go 指令集：插件源码由核心按能力表授权后下发，renderer 在独立作用域内执行，动作一律经 `POST /plugin-runtime/{action}` 受令牌约束。Monitor 的界面参考 `bfjnbvf/kimi-code-monitor@1d529f0b424a708971cfac097238df444f3b5beb`，MIT 归属与全文加入 `scripts/notices.py` 生成的依赖通知（该归属只出现一次，来自 `plugins/monitor/LICENSE` 与 `NOTICE`）。额度来自官方 `/oauth/usage`，仅 AVAILABLE 数据可显示已用比例；缓存自动化回执与真实命中指标分开，缺少样本保持未知。插件附件以当前会话暂存登记为边界。活动记录滚动保留 30 天，并合并短时间内相同网络事件。核心复用插件生命周期完成资源停止和故障隔离。

前端提供独立静态管理页和原生 surface ES 模块。mountEnhancement 保留独立页面嵌入能力；mountSurface 通过 createNativeHost 接入规划、多会话、设置和 Monitor。Shadow DOM 隔离样式，主题继承宿主变量。正式本地 RPC、宿主会话上下文、官方设置内嵌与原生布局已接入并通过隔离回归；后续需要覆盖宿主升级与其他平台。

自更新与插件热更新各有独立的可信来源。核心更新默认按 CNB 国内镜像优先、GitHub Releases 兜底的顺序读取 `latest/download/manifest.json`，再从同一源的 `download/v<版本>/` 取归档与 `SHA256SUMS`，验证清单 Ed25519 签名并核对清单、`SHA256SUMS` 和归档的 SHA-256 后交给独立监督进程替换并做健康检查，失败回滚旧核心。历史未签名清单在默认过渡模式可用，可设置 `EKD_UPDATE_REQUIRE_SIGNATURE=1` 强制验签。官方预装插件走固定的 `plugins-latest` 通道：清单内嵌 Ed25519 签名，客户端用内置公钥验签后才比较 `minCoreVersion` 与能力差异，未扩权直接热替换，新增能力需用户确认，已验签清单随插件记录持久化。

未来适配器版本失配只能停用对应能力。官方升级不影响独立核心启动。本机文件经 KAP 发送已验证，但系统文件选择窗口未 GUI 点选；网站上传、网站内对话、浏览器登录态恢复、真实账号自动化及其他平台运行仍按 [原生接入门槛](NATIVE_INTEGRATION.md) 独立验收。

## 研究来源与继承

本工程以本工作区的 `kimi-native-enhancement-research-20261110` 和 `kimi-stack-ab-20261110` 为输入，继承其契约、合成样本和异常用例。旧 Node/Rust 核心不进入产品依赖，也不维护平行核心。

一次性设计参考包括 [Magpie provider](https://github.com/yetone/magpie/blob/6a253f8af7537e17007f39f959f55932fceaf05d/internal/provider/provider.go)、[catalog](https://github.com/yetone/magpie/blob/6a253f8af7537e17007f39f959f55932fceaf05d/internal/catalog/catalog.go) 和 [usage](https://github.com/yetone/magpie/blob/6a253f8af7537e17007f39f959f55932fceaf05d/internal/usage/spend.go)。本工程不复用其明文 Key 文件、多供应商能力投票和未知单价忽略逻辑。模型字段只按明确来源合并，不发送推理探测。

原生接入依据 [官方固定源码](https://github.com/MoonshotAI/kimi-code/tree/1eb3c537eb2ae83b044e34356932edf492f83a3e) 中的 provider/model catalog、prompt 与 compact 契约，仍需合法配对及逐版本实机验证。官方 WebSocket 的 subscribe 可能触发恢复，因此不能把它当作无副作用的“只读观察”直接连接当前会话。`kimi app` 仅打开项目，不充当正式任务提交回执。
