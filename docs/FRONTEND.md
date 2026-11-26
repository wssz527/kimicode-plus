# 前端工作台

React + TypeScript + Vite 提供独立核心管理页与官方宿主内的原生 surface。开发时使用 Vite proxy；发布时 `scripts/build.sh` 同时构建普通 `web/dist` 和宿主 ES 模块 `web/dist-host`，复制到 `internal/webassets/dist` 后由 `go:embed` 内嵌。运行不需要 Node 或第二套业务核心；Electron/Chromium 能力来自官方宿主。

## 目录

```
web/
  index.html            Vite 入口，挂载 #root
  src/api.ts            /enhance/v1 客户端、认证与错误归一
  src/auth.tsx          配对状态、内存会话、启动时消费 #pair=
  src/App.tsx           路由分发与配对闸门
  src/AppShell.tsx      224px 侧栏、顶栏、窄屏导航
  src/PairingScreen.tsx 配对页与失败恢复
  src/pages/*.tsx       独立管理页页面（总览、用量、聊天、任务交接、插件、设置等；独立模型页与凭据页已移除）
  src/native.ts         原生 Host、按 scope 共享事件通道（含微任务合并与新增作用域即时补齐）、Core transport
  src/host-entry.tsx    独立 mountEnhancement、原生 mountSurface 与插件运行时导出
  src/components/Surfaces.tsx 共享 bootstrap、设置与会话导出
  src/components/SettingsSurface.tsx 「KimiCode+」状态/诊断标签页与探针回落小节
  src/components/OfficialSettingsSections.tsx 官方「账户」「智能体与会话」「实验室」「插件」页内的注入小节
  src/components/CacheSettings.tsx 「缓存与用量」标签页
  src/components/NativeSettings.tsx Kimi Code 原生配置与官方模型目录下拉的子代理模型池
  src/components/ModelSelect.tsx 统一模型选择组件（子代理池、聊天与插件共用）
  src/components/PluginSettings.tsx 插件列表、详情、启停安装与插件凭据入口
  src/components/PluginCredentials.tsx 插件申请凭据的添加、替换与删除面板（不回显明文）
  src/components/AccountsSection.tsx 官方「账户」页内的多账号切换与钥匙串状态
  src/components/SessionGrid.tsx 最多三个同屏会话，按窗格数恢复本机宽度比例
 src/components/workbench/NativeComposer.tsx 同屏会话输入、附件与发送控制
 src/components/workbench/ApprovalCard.tsx 工具审批、反馈与问答交互
  src/components/workbench/SessionPanes.tsx 会话窗格与输入区
  src/components/workbench/ControlMenu.tsx 窗格头部菜单与模型/权限选择
  src/components/workbench/pieces.tsx 窗格共享的小组件与图标
  src/components/       图标与共享 UI 原语
  src/lib/              纯函数：合同、用量读数、预览差异渲染、models.dev 目录校验、脱敏、格式化
  src/styles/           设计 token、共享组件样式与页面样式
  public/fonts/         自托管 Noto Sans SC 子集与 JetBrains Mono 数字子集（OFL）
  tests/                Node --test 用例（tsc 编译后运行）
adapters/
  types.ts              DesktopAdapter / BrowserAdapter / ChatAdapter / Gateway / Credential 接口
  canonical.ts          规范 JSON 与 SHA-256
  synthetic-chat.ts     四个合成站点夹具，extract / prepareDraft / 回读
  synthetic-desktop.ts  合成宿主，意图幂等与 UNKNOWN
  browser.ts            仅生成 HTTPS 外链，rel=noopener noreferrer
```

## 命令

```sh
cd web
npm ci                # 严格按 package-lock 安装
npm run typecheck     # tsc --noEmit
npm test              # tsc -p tsconfig.test.json && node --test
npm run build         # 输出 web/dist
npm run build:host    # 输出 web/dist-host 宿主 ES 模块与样式
```

开发代理默认指向 `http://127.0.0.1:43125`（与 README 的示例端口一致），可用 `ENHANCE_DEV_TARGET` 覆盖：

```sh
(cd web && npm run dev)                                    # 代理到 127.0.0.1:43125
ENHANCE_DEV_TARGET=http://127.0.0.1:8899 npm run dev       # 覆盖目标
```

浏览器在 `localhost:5173` 发出的写请求会带 `Origin: http://localhost:5173`，而核心只接受同源 Origin。Vite 代理的 `changeOrigin` 只改 Host，因此代理配置另外把存在的 Origin 改写为代理目标源；这只发生在开发代理里，生产 `Guard` 的 Host/Origin 校验没有被放宽。

## 认证与安全

- 启动时读取 `location.hash` 的 `#pair=<token>`，**立即** `history.replaceState` 清除地址栏片段，再 `POST /enhance/v1/pair`。
- `sessionToken` 只保存在 `api.ts` 的模块级变量中；不写 localStorage / sessionStorage / cookie / IndexedDB。
- 刷新或关闭页面即丢失会话，需要重新粘贴 `runtime.json` 中的 `pairingToken`。
- 所有请求带 `Authorization: Bearer`、`credentials: omit`、`cache: no-store`、`referrerPolicy: no-referrer`。
- 401 自动清除内存会话并回到配对页。
- 核心要求写请求 `Content-Type: application/json`；聊天发送另需 `Idempotency-Key`。
- 开发代理默认 `http://127.0.0.1:43125`；代理会把浏览器带来的 Origin 改写为目标源，生产 Guard 不受影响。

## 原生 surface

`mountSurface` 将规划、会话、设置或 Monitor 挂入宿主提供的元素。`createNativeHost` 通过主进程 loopback RPC 调用真实动作，`events.poll` 按 session/规划 scope 分发更新；`createCoreTransport` 将 Core 请求交给主进程 `core.fetch`，不向 renderer 暴露 Bearer token。设置窗口由 AuthProvider 包裹，但已安装 transport 时不需要再次手工配对。

原生功能边界：统一规划入口默认网页，来源、地址与配置放菜单；本地规划有流式、停止、预设、工具审批和交接，网页只提取增强自建页面的选区/正文并交接 Code，不是网站内自动聊天。菜单、设置及会话选择弹层出现时隐藏网页子视图。多会话最多三个，同一入口逐次追加。主窗格跟随官方侧栏，已显示会话交换位置。`SessionInputProvider` 按会话保存草稿、模型、权限、发送模式、思考和待发附件，本机存储按两窗格或三窗格恢复比例。`NativeComposer` 的同一个 36 px 按钮切换发送与停止。附件可通过文件选择、拖入或粘贴添加，每条消息最多 16 个附件。无本机路径的剪贴板图片以内联方式发送，合计上限 1 MiB。

宿主将 `--radius-composer`、`--color-composer-*` 和分割线变量桥接到影子树。缺少圆角变量时使用 32 px 回退值，并在 `dom.composer_style` 显示降级诊断。官方头部尺寸由 `ResizeObserver` 跟踪。消息按回合显示流式思考和结构化工具状态及输入输出。代码块支持复制和自动换行。距底小于 80 px 时新消息会跟随到底部，向上阅读时可通过「回到底部」恢复跟随。

同屏通过 `session.send` 的 `clientMessageId` 去重，同一发送意图重试时复用标识。核心以会话 ID 和消息标识为键，通过两分钟内存窗口复用已接收或送达状态不明的结果。审批卡接受可选反馈，未知自定义选项须明确选择允许或拒绝，问答卡按问题 ID 提交 `answers`。

设置不再使用单层 5 项列表加子页返回，改为官方原生标签归位：官方「账户」页顶部由 `AccountsSection.tsx` 提供多账号列表与切换（钥匙串可用时直接显示免密解锁状态）；官方「模型供应商」模型行由 `internal/desktop/assets/provider_enhance.js` 提供能力摘要、独立检查器和批量配置；官方「智能体与会话」页由 `NativeSettings.tsx` 注入子代理模型池、子代理时限、单轮步数上限与权限规则；官方「实验室」页由 `OfficialSettingsSections.tsx` 注入会话并排与规划实验开关；官方「插件」页新增 KimiCode+ 分段承载增强插件的安装、启停与授权（宿主同步采用有界激活等待避免单个慢速/挂起插件阻塞其余插件开关，`loadPluginAsset` 与核心资产接口按 `version`/`updatedAt`/`capabilities` 强绑定修订并在导入与缓存前 fail-closed 核验，`mountPluginSettings` 按面板所展示的目标修订等待同步与激活、遇 `PLUGIN_REVISION_MISMATCH` 自动重同步并有限重试且响应 `AbortSignal` 取消）；独立的「缓存与用量」标签页由 `CacheSettings.tsx` 承载缓存、保活、压缩与实际用量；「KimiCode+」标签页由 `SettingsSurface.tsx` 提供状态与诊断、功能归位索引，以及任一官方页探针失效时的回落小节。凭据增改删嵌入申请该凭据的插件详情页（`PluginCredentials.tsx`）。`CacheSettings`、浏览器权限复用已有 `Toggle`，缓存保存后读回再更新草稿基线。opaque origin 的请求 ID 使用随机回退；缓存、布局、权限、供应商能力补齐和子代理模型池的实机流程已通过。

原生视觉沿用官方右侧面板、主聊天区和侧栏结构，不再使用常驻工作台外壳、会话选择栏、空闲状态带或多会话常驻底部恢复栏。renderer 通过容器属性与 CSS 持续隐藏 Vue 动态重建的官方子节点，原生工具/标签切换时释放。插件 `ui:entry` 同时注入官方“会话信息 → 可打开”、右侧空白面板列表与 `+` `browser-overlay` 菜单，`ui:sidebar-nav` 注入左侧栏“新建会话”下方；`Monitor`（`plugins/monitor/`）按上游桌面客户端版复刻模块化面板、Rive 蓝球形象与三区拖拽，并展示 Kimi Code 额度、加油包与外部供应商余额。自动化总开关与额度恢复自动继续策略统一位于「缓存与用量」，三项均默认关闭。

## 页面

| 页面 | 路径 | 主要接口 |
| --- | --- | --- |
| 增强设置 | `/` | health |
| 诊断总览 | `/?mode=diagnostic` | health、tasks、usage/summary、plugins、quotas、gateways |
| 用量 | `/usage` | usage、usage/summary、usage/demo、quotas、gateways |
| 聊天 | `/chat` | chat/sources、chat/threads、chat/threads/{id}/messages、desktop/actions/models.list |
| 任务交接 | `/handoff` | sessions、tasks/preview、tasks、tasks/{id}/reconcile |
| 插件 | `/plugins` | plugins、plugins/{id}、credentials（插件详情内嵌） |
| 设置 | `/settings` | settings、chat/sources、health |

独立首页提供核心设置和增强插件入口。用量、聊天、旧任务交接、会话与执行策略及浏览器会话可从诊断工作台访问。页面通过 Go API 读取数据，明确标注的 Mock 流程使用本地夹具。原生规划、定时任务与 KAP 会话使用宿主通道。

导航使用 `useSyncExternalStore` 订阅 `window.location`：内部 `navigate()` 会显式通知订阅者，浏览器前进/后退由 `popstate` 驱动，因此 URL、页面与标题始终同步。没有引入路由库。

## 关键交互

**供应商模型能力与子代理池（原生增强）**。在官方「模型供应商」保存模型后，点击模型行的能力摘要打开检查器。图片、视频和工具可以多选。思考模式分为关闭、可切换和始终思考。可切换模式提供自适应思考开关。支持的强度可以多选，已有自定义能力可见并随配置保留。最大输出支持整数、K 和 M，留空恢复模型默认值，且不能超过上下文。可在检查器中同时修改上下文和输出上限。仅编辑能力时，已保存的上下文保持原值。

- 点击「填入目录建议」会读取 `models.list` 的同供应商、同模型条目，并在 models.dev 公共目录核对供应商 HTTPS 端点与完整模型 ID。首次需要公共目录时复用 registry 的固定来源和缓存机制。公共条目缺少 API 地址时，仅使用 registry 对应该供应商的官方预设地址。确认填入的草稿后点击「保存」。
- 点击列表顶部的「批量配置」，选中同一供应商下的已保存模型，再打开「编辑能力模板」。模板替换所选模型的已知输入、工具和思考配置。已保存的上下文、最大输出及自定义能力随原模型保留。
- `config.get` 保留配置顺序并移除 API 密钥、OAuth 对象和可携带凭据的自定义头与环境变量。`providers.updateModel` 接收 `{providerId, model, edit}`，`providers.updateModels` 接收 `{providerId, models: [{model, edit}]}`。`edit` 中省略的字段按已保存值合并。`max_output_size: null` 清除显式输出上限。批量更新会先验证所有目标，经一次官方写入后逐项读回。只有核对通过才提示保存成功。Kimi Code 1.4.3 的官方表单全量提交模型配置，可能清除输出上限或覆盖检查器刚保存的能力。需要同时调整上下文与输出时，在检查器中一并保存；检查器修改上下文会同步更新当前打开的官方表单输入框。表单挂载时增强层同步从官方 Pinia 内存配置捕获打开快照并记录当时的官方服务 Origin，仅拦截无子路径且匹配当前官方 API Origin 的 `PUT /api/v1/providers/{id}`；若打开后连接的官方服务实例发生漂移则直接返回 409 冲突中止保存，否则附带 `expectedOrigin` 经后端 `providers.saveOfficialForm` 校验 Origin 并在同一 `providerModelsMu` 锁内读取最新权威配置、区分官方 `$J` 省略字段与显式编辑后原子合并写回（KimiCode+ 检查器与官方表单的并发保存严格串行；绕过 KimiCode+ 直接外部改写配置文件则遵循后写生效），`max_context_size` 低于已存 `max_output_size` 时自动省略超限输出上限。Fetch `AbortSignal` 经增强层、宿主桥接与主进程端到端透传：若在后端派发官方 `PUT` 之前（含等待保存锁期间）中止，请求直接按标准 Fetch 规范 reject `AbortError` 且不会写入后端；若官方守护进程已收到 `PUT` 后才中止，客户端立即停止等待并 reject `AbortError`（不刷新打开快照），但守护进程仍可能完成该次写入。若后端保存或合并失败，增强层中止本次提交并返回错误信封提示重试，绝不回退旧快照或降级直发未合并的 `PUT`。

子代理模型池从 `models.list` 读取官方已配置模型目录，支持拖拽调整 `1、2、3…` 优先级。保留官方默认子代理设置，并按完整替换保序写回 `secondary_model`。已保存但当前目录缺失的模型继续显示并标注。

**用量**：读数和展示全部来自核心。顶部读数用 `headlineFromSummary` 直接取 `/usage/summary`：`known=0` 显示 UNKNOWN（绝不以 0 代替），全部事件已知时显示精确值（0 也是有效值），只有部分已知才显示“≥ 部分和”。本地事件列表只代表当前页，不参与全历史合计。`estimate` 是对象，`partialKnown` 是 `{sum,known,total,complete}`。

**费用只有一个来源**：核心 summary 的 `estimate`。浏览器不再做任何计费：分组卡片只显示请求数与 Token，事件表不显示单条金额，事件详情只把 `price` 作为原始 JSON 展示。分项费用界面明确标注“未提供”，不用 UNKNOWN 假装接口返回过该字段。

“写入核心合成演示”只调用 `POST /usage/demo`，随后重新读取核心的 events、summary 与 quotas；不存在前端平行样本，也没有前端计费分支。核心确定性写入本机独立 SQLite（含一次真实重试与 Mock 额度快照），重复调用只增加 `duplicates`。历史事件通过 `?offset&limit` 分页（每页 50），筛选与汇总都由核心执行，分组与七天图只统计当前页并在界面说明。安全模式下允许这类本地数据写入是预期行为。

**交接（旧 Outbox 页面）**：选消息 → 填目标/diff/附件 → 选方向和模式 → 预览（核心脱敏任务包 + findings + confirmationHash）→ 确认提交。附件只读取用户主动选择的文件字节并计算 SHA-256，单文件 ≤ 1 MiB，最多八个、总量 ≤ 8 MiB。`api.attachments.upload` 逐文件二进制上传，任务仅携带返回的 blobRef/哈希/大小，不再将全部 base64 塞进任务 JSON；legacy dataBase64 仍由核心兼容。

界面始终渲染 `preview.redacted`，提交使用内存中的规范任务与 `confirmationHash`，核心重新校验附件引用和脱敏哈希。附件字节保存于私有 SQLite blob store，不回显到任务 JSON；STORED 只表示本地保存，目标逐附件回执才表示送达。该旧 Outbox 的目标仍为 Mock，与原生 KAP 路径不共享上传或回执语义。

续接的选中状态保存 Outbox **记录 ID**（不是链根 taskId），提交时再从该记录推导 `linkedTaskId`；手动选择历史项会同步原 `sessionId`/`projectId`。候选只列 `DELIVERED` 且带 `checkpointHash` 的记录，默认选每条链最新已交付父并标注旧父；旧父提交会被 `CHECKPOINT_BRANCH` 拒绝。核对成功后用返回的最新记录更新回执横幅，而不是只刷新列表。Mock 故障行为：`TIMEOUT`/`EMPTY`/`PANIC` 落 `UNKNOWN` 并保留核对入口；`OCCUPIED` 时宿主根本没有接受，核心返回 409 `SESSION_OCCUPIED` 并标 `REJECTED`，没有回执也没有检查点可核对，确认未接受后可用新意图重试。

**聊天（独立管理页）**：进入页面时按核心 `/settings` 的 `defaultChatSource` 初始化选中来源（默认仍是 Kimi），且只在首次初始化应用，之后不覆盖用户的手动选择。设置值不在来源列表中时回退到 Kimi 或第一个已知来源，并显示可恢复的提示；读取设置失败也回退并说明。外部 HTTPS 入口使用安全新标签属性；配置 providerId 的独立线程走真实 Go 供应商协议，旧无线程 provider 的线程明确为 Mock。此独立轻量聊天不声称流式或网站内发送；原生规划和网页交接能力见上方 surface。

**插件**：只管理内置模拟插件；未启用的插件不能触发故障（与核心 `PLUGIN_NOT_ENABLED` 一致）；安全模式下启停按钮禁用并说明原因。

**设置**：独立核心设置保存携带 revision，冲突时提示重新读取。25 分钟是旧策略的默认值，不是官方 TTL；原生自动化三项默认关闭，后台调度与回执已接入，不能把 ACK 或保存成功当作压缩/恢复完成。

## 适配器边界

`adapters/types.ts` 保留五个接口，不实现真实站点抓取、登录态读取或官方任务提交：

- `DesktopAdapter`：状态、会话、提交、核对。合成实现按 intent 幂等，同 intent 不同内容报冲突，UNKNOWN 不重投。
- `BrowserAdapter`：只构造 HTTPS 外链，拒绝凭据、非 HTTPS 与自定义协议。
- `ChatAdapter`：`detect` 永远返回 `UNVERIFIED`；`extract` 只返回选中消息并标记 partial；`prepareDraft` 校验 expectedHash 并回读；附件一律 unavailable。
- `GatewayAdapter` / `CredentialProvider`：余额与凭据描述始终 UNKNOWN / 只描述不暴露。

真实站点选择器、登录态、附件下载与官方 prompt 回执均未验证，相关按钮和文案不声称可用。

## 测试

`npm test` 编译并运行前端用例；实际数量以当前测试输出为准，不在文档里写死。原生 Host 回归另覆盖官方主会话跟随、交换去重、请求与 scope 订阅。覆盖：

- 合同构造：pair body、列表包装、错误体、usage 查询、任务提交 body、供应商更新 body、分页查询、revision 校验、幂等键
- 导航：路径规范化、路由解析、订阅/取消订阅通知
- 用量读数：空账本 UNKNOWN、全未知、部分已知 0、完整 0、summary 缺失
- 任务包校验：三种方向、续接 parentHash/checkpoint、角色集合、消息选择子集、附件 SHA-256 与 1 MiB 上限
- 适配器：规范 JSON、SHA-256、detect/extract/partial、草稿 expectedHash 与回读、意图幂等与 UNKNOWN、外链安全属性
- 用量与模型：Go 预览差异渲染（新增/更新/来源变化/无变化，不虚构删除）、`retrieved_at` 来源与两种目录形态、规范 count 字段（不再解析 raw）、费用只取 Go summary、分组与直方图

独立桌面 1280×720 的历史截图与窄屏检查只覆盖旧管理页。`../work/ekd-final-integration/` 下的脚本记录的是设置归位之前的层级：`verify-review-fixes.mjs` 检查动态容器、官方侧栏跟随、追加同屏、深浅/窄窗、当时的单层设置与 Monitor；`verify-settings-flow.mjs` 检查布局/权限保存恢复及模型目录；规划、多会话、网页、附件和手动恢复各有独立业务脚本。现行原生标签归位（账户、智能体与会话、实验室、插件页 KimiCode+ 分段、缓存与用量、KimiCode+ 诊断页）以及顶栏更新胶囊、账号切号与 KCT 译文样式在 v0.2.8 发布前逐页复核，结果记录在 `docs/VALIDATION.md`。网页使用真实子视图截图和坐标核对；附件 KAP 发送是真实调用，但系统选择窗口使用确定性返回值，未 GUI 点选。主 WebContents 不能捕获子 WebContentsView，当前录屏权限也未取得完整窗口合成截图；因此这里不宣称已有完整窗口级截图。
