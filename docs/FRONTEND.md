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
  src/components/SettingsSurface.tsx 官方设置内的单层五项列表与子页
  src/components/NativeSettings.tsx Kimi Code 原生配置与官方模型目录下拉的子代理模型池
  src/components/ModelSelect.tsx 统一模型选择组件（子代理池、聊天与插件共用）
  src/components/PluginSettings.tsx 插件列表、详情、启停安装与插件凭据入口
  src/components/PluginCredentials.tsx 插件申请凭据的添加、替换与删除面板（不回显明文）
  src/components/SessionGrid.tsx 最多三个同屏会话
  src/components/workbench/ 规划、网页与会话组件
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

原生功能边界：统一规划入口默认网页，来源、地址与配置放菜单；本地规划有流式、停止、预设、工具审批和交接，网页只提取增强自建页面的选区/正文并交接 Code，不是网站内自动聊天。菜单、设置及会话选择弹层出现时隐藏网页子视图。多会话最多三个，同一入口逐次追加；主窗格跟随官方侧栏，已显示会话交换而不重复，`SessionInputProvider` 以 session ID 保存草稿、模型、权限和附件，切换位置不丢状态；多会话输入框与官方单会话保持一致（仅展示发送权限与模型，不含额外的发送方式下拉框）。消息复用 Markdown 渲染、时间、复制与引用回复；附件加号调用系统文件选择，文件通过真实 KAP prompt 发送。

设置首页当前为单层 5 项列表（缓存与额度、Kimi Code 配置、插件、附加功能、状态与诊断），子页带返回，嵌入官方设置对话框的 KimiCode+ 标签，位于“已归档”前；已移除独立管理窗口跳转行、七分类、嵌套胶囊与 `.ekds-embed` 硬嵌页面（后续按 N6 归位到官方对应设置页并平铺 KimiCode+ 三节）。官方“模型供应商”模型行通过 `internal/desktop/assets/provider_enhance.js` 行内展开“能力与限制”面板，补齐图片/视频/工具能力、思考强度（`no` / `low` / `medium` / `high` / `xhigh` / `max`）与上下文/最大输出长度（保存时无损保留已有的 `adaptive_thinking` 与未知字段）；“Kimi Code 配置”（`NativeSettings.tsx`）仅管理子代理模型池、子代理时限、单轮步数上限与权限规则，不重复模型能力编辑页；凭据增改删嵌入申请该凭据的插件详情页（`PluginCredentials.tsx`）。`CacheSettings`、浏览器权限复用已有 `Toggle`，缓存保存后读回再更新草稿基线。opaque origin 的请求 ID 使用随机回退；缓存、布局、权限、供应商能力补齐和子代理模型池的实机流程已通过。

原生视觉沿用官方右侧面板、主聊天区和侧栏结构，不再使用常驻工作台外壳、会话选择栏、空闲状态带或多会话常驻底部恢复栏。renderer 通过容器属性与 CSS 持续隐藏 Vue 动态重建的官方子节点，原生工具/标签切换时释放。插件 `ui:entry` 同时注入官方“会话信息 → 可打开”、右侧空白面板列表与 `+` `browser-overlay` 菜单，`ui:sidebar-nav` 注入左侧栏“新建会话”下方；`Monitor`（`plugins/monitor/`）按上游桌面客户端版复刻模块化面板、Rive 蓝球形象与三区拖拽，并展示 Kimi Code 额度、加油包与外部供应商余额。自动化总开关与额度恢复自动继续策略统一位于“缓存与额度”，三项均默认关闭。

## 页面

| 页面 | 路径 | 主要接口 |
| --- | --- | --- |
| 总览 | `/` | health、tasks、usage/summary、plugins、quotas、gateways |
| 用量 | `/usage` | usage、usage/summary、usage/demo、quotas、gateways |
| 聊天 | `/chat` | chat/sources、chat/threads、chat/threads/{id}/messages、desktop/actions/models.list |
| 任务交接 | `/handoff` | sessions、tasks/preview、tasks、tasks/{id}/reconcile |
| 插件 | `/plugins` | plugins、plugins/{id}、credentials（插件详情内嵌） |
| 设置 | `/settings` | settings、chat/sources、health |

独立管理页的六个主页面全部调用真实 Go API（原独立 `/models` 与 `/credentials` 页面及路由已按修正轮 R2 移除），没有前端写死的业务数据；只有明确标注为 Mock 的部分（合成站点夹具、旧交接来源夹具、Mock 适配器）是本地构造。原生规划、定时任务与 KAP 会话走独立真实路径。用量页面没有本地示例事件。

导航使用 `useSyncExternalStore` 订阅 `window.location`：内部 `navigate()` 会显式通知订阅者，浏览器前进/后退由 `popstate` 驱动，因此 URL、页面与标题始终同步。没有引入路由库。

## 关键交互

**供应商模型能力与子代理池（原生增强）**：在官方“模型供应商”模型行点击“能力与限制”行内展开面板，先经 `provider.get` 读取完整供应商配置，仅修改目标模型的图片/视频/工具能力、思考强度（选 `no` 时关闭思考且不写入字面 `"no"`）、上下文长度与最大输出长度后整体写回（保留原密钥不回传明文，并无损保留已有的 `adaptive_thinking`、`custom_headers`、`env` 及未知字段），再次读回核对一致才提示已保存。子代理模型池从 `models.list` 读取官方已配置模型目录并支持拖拽调整 `1、2、3…` 优先级顺序（移除冗余“默认”单选列，保留官方默认子代理设置并按完整替换保序写回 `secondary_model`）；已保存但当前目录缺失的模型保留显示并标注提示。

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

`npm test` 编译并运行前端用例；最近一轮原生返修为 61 项，实际数量以当前测试输出为准。原生 Host 回归另覆盖官方主会话跟随、交换去重、请求与 scope 订阅。覆盖：

- 合同构造：pair body、列表包装、错误体、usage 查询、任务提交 body、供应商更新 body、分页查询、revision 校验、幂等键
- 导航：路径规范化、路由解析、订阅/取消订阅通知
- 用量读数：空账本 UNKNOWN、全未知、部分已知 0、完整 0、summary 缺失
- 任务包校验：三种方向、续接 parentHash/checkpoint、角色集合、消息选择子集、附件 SHA-256 与 1 MiB 上限
- 适配器：规范 JSON、SHA-256、detect/extract/partial、草稿 expectedHash 与回读、意图幂等与 UNKNOWN、外链安全属性
- 用量与模型：Go 预览差异渲染（新增/更新/来源变化/无变化，不虚构删除）、`retrieved_at` 来源与两种目录形态、规范 count 字段（不再解析 raw）、费用只取 Go summary、分组与直方图

独立桌面 1280×720 的历史截图与窄屏检查只覆盖旧管理页。当前脚本与 `review-*.png` 位于 `../work/ekd-final-integration/`：`verify-review-fixes.mjs` 检查动态容器、官方侧栏跟随、追加同屏、深浅/窄窗、单层设置与 Monitor；`verify-settings-flow.mjs` 检查布局/权限保存恢复及模型目录；规划、多会话、网页、附件和手动恢复各有独立业务脚本。网页使用真实子视图截图和坐标核对；附件 KAP 发送是真实调用，但系统选择窗口使用确定性返回值，未 GUI 点选。主 WebContents 不能捕获子 WebContentsView，当前录屏权限也未取得完整窗口合成截图；因此这里不宣称已有完整窗口级截图。
