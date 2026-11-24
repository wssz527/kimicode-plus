# 插件开发说明（插件系统 v1 / v2）

> 版本关系：`apiVersion: 1` 的清单与运行时接口完全向后兼容，Monitor、KCT 和基础示例插件无需改动。`apiVersion: 2` 开放完整的界面挂载点（左侧栏一级导航、主聊天区整页/分栏、右侧面板标签与弹出窗、会话信息与 `+` 菜单入口、侧栏折叠协作、独立隔离网页视图）以及会话控制、模型目录、附件、规划、交接、命令和定时任务等全部核心能力。v1 与 v2 能力开放给第三方本地插件，由用户在安装时逐项勾选授权并对高权限能力作二次风险确认。Windows 宿主能力以运行时的 `platformLimitations` 为准。

KimiCode+ 支持本地受信增强插件。插件是运行在宿主渲染进程里的 ES module，可以贴合官方界面，并通过核心提供的接口取用缓存指标、会话、配置、模型、网页视图和网络代理。核心在 Go 端按插件身份与已授权能力强制鉴权，渲染层的校验作为第二道防线。

能力与安全边界：

- 安装方式：支持从**本地文件夹或 `.zip` 包**安装（设置页可直接唤起系统目录/文件选择器，或粘贴路径）。不提供第三方在线插件市场。
- 运行环境：不做不可信沙箱。插件运行在宿主渲染进程中，与 KimiCode+ 共享同一运行环境，**可以读取当前界面内容**，没有独立的系统沙箱，除界面会话内容外还可能触及本机的增强能力与接口。因此只应安装来源完全可信的第三方增强插件；安装界面与权限清单会如实提示，能力清单用于声明与管理插件能调用哪些接口，不能替代对插件代码的信任审查。
- 完整性保护：预装插件持续对照二进制内嵌的构建期内容摘要；第三方本地插件在安装完成时固化目录内容摘要（`install_digest`），每次调用高权限能力前重新核对磁盘内容，防止安装确认后被静默篡改提权。开发者更新本地代码后，重新执行本地覆盖安装即可刷新摘要并重新确认授权。
- 官方在线热更新：预装插件还可通过内置公钥验签的 `plugins-manifest.json` 更新。验签通过的清单按插件 ID 与版本记录在插件记录里（`signedOfficialDigest`），高权限调用时与构建期内嵌摘要一样能证明文件来自官方发布，因此热更新后的 v2 高权限插件不需要重新走第三方安装确认。
- 核心解耦：Monitor、KCT、规划、定时任务都运行在这套标准接口之上，关闭全部插件后核心功能仍完整可用。

## 包结构

```
<插件目录>/
  plugin.json      # 清单，必需
  renderer.js      # ES module，导出 activate(ctx)
  settings.js      # 可选，导出 renderSettings(ctx, element)
  assets/          # 可选
  LICENSE          # 必需
```

## plugin.json

```json
{
  "id": "kimicode-plus.example-hello",
  "name": "示例插件",
  "version": "1.0.0",
  "apiVersion": 1,
  "description": "最小示例插件。",
  "author": "KimiCode+",
  "license": "MIT",
  "hostVersions": ["1.*"],
  "entry": { "renderer": "renderer.js", "settings": "settings.js" },
  "capabilities": ["ui:sidebar", "ui:settings", "storage"],
  "upstream": { "repo": "owner/repo", "commit": "1d529f0", "license": "MIT" }
}
```

字段规则：

| 字段 | 规则 |
|---|---|
| `id` | `发布者.名称`，两段均为 `[a-z0-9][a-z0-9-]{0,31}`。安装目录以它命名。 |
| `name` | 1–80 个字符。 |
| `version` | `major.minor.patch`。 |
| `apiVersion` | 必须为 `1` 或 `2`。其他版本直接拒绝，不做猜测。v2 清单才能声明下节的高权限能力。 |
| `hostVersions` | 1–32 项。写具体版本（`1.4.2`）或点边界通配（`1.*`、`1.4.*`）。**指官方 Kimi Code 客户端版本**，不是 KimiCode+ 的版本。当前人工验证基线为官方 Kimi Code 1.4.2 / Electron 45.1.0（macOS & Windows）；更早的 1.3.5/1.3.7 记录保留在 `docs/VALIDATION.md`。 || `entry.renderer` | 必需，相对路径，`.js`，必须位于插件目录内。 |
| `entry.settings` | 可选，同上。 |
| `capabilities` | 0–64 项，见下节。未知名称、重复项、空项都会被拒绝。 |
| `LICENSE` | 文件必须存在。 |

清单解析是严格的：未知字段、对象后的多余内容、超过 64 KiB 都会被拒绝。这样写错一个能力名会立刻失败，而不是被静默忽略后少授权。

**版本门禁**：宿主客户端版本不在 `hostVersions` 内时，插件保持停用并显示"未在此版本验证"。宿主版本未知时（例如尚未接入官方客户端）不做拦截。

## 能力清单 v1

| 能力 | 提供内容 |
|---|---|
| `metrics:read` | `cache.summary`、`cache.sessions`、`cache.history`、`cache.turns`、`cache.retention`、`cache.receipts`、`quota.balances`，以及 `cache`/`automation`/`activity` 事件订阅。 |
| `sessions:read` | `sessions.list`、`sessions.view`，以及 `session:<id>` 事件订阅。 |
| `sessions:send` | `sessions.send`、`sessions.create`。每次发送写入活动记录。 |
| `config:read` | `config.get`。 |
| `config:write` | `config.save`。每次修改写入活动记录。 |
| `ui:sidebar` | 侧栏底部挂载点。 |
| `ui:settings` | 插件详情里的设置子页。 |
| `ui:thinking` | 思考内容块装饰点。 |
| `ui:message` | 消息块装饰点。 |
| `net:<host>` | 经 Go 代理访问指定主机。`net:*.example.com` 只匹配一层子域，**不包含裸域**。 |
| `credentials:<name>` | 让核心在出站请求里注入凭据库中的条目。**明文不进入渲染进程**。 |
| `storage` | 插件私有键值存储（SQLite），只有该插件可读写。 |

授权是 fail-closed 的：能力表里没有的 action 一律拒绝；零值授权什么都不授予；用户安装时可以确认比清单**更少**的能力，未确认的能力不会被授予。

未授权时返回 `403 PLUGIN_CAPABILITY_DENIED`。

## 能力清单 v2（扩展挂载点与高权限能力，向第三方插件全面开放）

第三方插件只需在 `plugin.json` 中将 `"apiVersion"` 设为 `2`，即可申请下表中的所有 UI 挂载点与核心能力。授权与完整性机制如下：

- **安装期二次确认**：用户在官方设置「插件 → KimiCode+」分段点击「安装本地增强插件」，勾选任何 v2 高权限能力时，界面会给出明确风险提示，要求勾选风险确认框（请求体携带 `confirmPrivileged: true`），未确认会被核心拒绝（`400 PLUGIN_PRIVILEGED_CONFIRM_REQUIRED`）。
- **内容摘要保护**：
  - 预装插件：延续构建期 `plugins/registry.go` 计算的 SHA-256 摘要白名单，每次高权限调用都会重新核对磁盘内容。
  - 官方热更新后的预装插件：内置公钥验签通过的 `plugins-manifest.json` 提供该版本的 `contentDigest`，与构建期白名单同等地通过高权限复核；清单随插件记录持久化，重启后仍有效。签名无效、版本对不上、或内置副本版本更新时，该摘要不被采信，插件退回构建期白名单校验。
  - 第三方本地插件：安装落盘时计算并持久化目录内容摘要（`install_digest`），每次高权限调用前重新核对磁盘内容。若安装后文件在磁盘上被外部静默篡改，摘要失配会立即拒绝高权限调用（`403 PLUGIN_PRIVILEGE_DENIED`）；开发者修改插件源码后，重新执行本地安装即可更新摘要并重新确认授权。

| 能力 | 提供内容 | 对应动作 |
|---|---|---|
| `ui:entry` | 一种"可打开标签类型"：会话信息「可打开」分组、右侧面板空白列表和 `+` 菜单的三处入口。按容器幂等注入，官方重渲染后自动补回 | 运行时 `ctx.entry.add({ id, title, icon, open })`，无独立动作 |
| `ui:sidebar-nav` | 官方左侧栏「新建会话」下方的一行入口，结构和样式复制官方侧栏行；点击行为由插件决定（通常是打开 `ui:main-area` 页面） | 运行时 `ctx.sidebarNav.add({ id, title, icon, open })`，无独立动作 |
| `ui:main-area` | 主聊天区右侧分栏（`mode: "split"`，可拖动分隔条）或占满整个主聊天区（`mode: "page"`，退出后恢复原生视图） | 运行时 `ctx.mainArea.mount()` |
| `ui:right-panel` | 右侧面板动态标签页，带关闭键，宽度沿用官方侧边聊天；可弹出独立插件窗口 | 运行时 `ctx.rightPanel.mount()`、`ctx.window.popout()`；`ui.popout` |
| `ui:layout` | 按引用计数收起左侧栏，全部释放后恢复原状 | 运行时 `ctx.layout.acquireSidebarCollapse()` |
| `sessions:approve` | 处理会话审批/提问 | `sessions.approve` → 原生 `session.reply` |
| `sessions:abort` | 停止运行中的会话 | `sessions.abort` → 原生 `session.abort` |
| `sessions:history` | 真正分页的历史回合（官方 `before_ordinal` + `page_size`） | `sessions.history` |
| `sessions:read`（v1 已有） | 会话列表与读取；显式引用计数的实时订阅；打开已读取的官方会话；读取会话工作区 Git Diff（已脱敏） | `sessions.list/view/diff/open/watch/unwatch` |
| `attachments` | 系统文件选择、拖入文件暂存与发送附件 | `attachments.stage/release/send`（发送经 `sessions.send`） |
| `browser:view` | 创建、导航、后退、前进、刷新、显隐、销毁隔离网页视图；读选区/正文；执行站点适配器；在系统浏览器打开当前页 | `browser.open/navigate/back/forward/reload/bounds/visible/extract/adapter/openExternal/close` |
| `planner:run` | 本地规划引擎：线程、模型、流式、审批 | `planner.*` |
| `handoff` | 交接预览与提交（新/已有会话，普通/Plan/Tower） | `handoff.preview`、`handoff.commit` |
| `models:read` | 可用模型列表（含供应商、能力、自适应思考、上下文/输出上限与推理强度） | `models.list` |
| `schedule:manage` | 核心持久化定时任务引擎：任务增删改停、立即运行、运行记录与未读状态，以及 `ctx.ui.pickDirectory()` 原生工作目录选择 | `schedule.status/list/save/delete/toggle/runNow/runs/markRead/markAllRead/prefs` |
| `commands` | 插件命令注册与跨插件调用 | `commands.register/unregister/list/invoke` |

约束：

- 第三方（本地文件夹或 zip）安装时提交高权限能力且未传 `confirmPrivileged: true` 会返回 `400 PLUGIN_PRIVILEGED_CONFIRM_REQUIRED`；确认后与预装插件共享同一套能力鉴权与运行时挂载点。
- 磁盘内容摘要不匹配时，`runtime.info` 不再列出高权限能力，运行时因此不会渲染对应界面；即使插件绕过界面直接调用，Go 门禁也会返回 `403 PLUGIN_PRIVILEGE_DENIED`。
- 停用、卸载、故障、安全模式和核心关闭都会撤销身份令牌，并同时释放该插件的网页视图、命令注册与会话订阅；这些停止路径都会结束该插件创建且仍在运行的规划线程（历史保留）。
- `commands` 只做身份与归属校验，命令本体在插件自己的渲染上下文里执行。调用不存在的命令返回 `404 PLUGIN_COMMAND_UNKNOWN`，所属插件未运行返回 `409 PLUGIN_COMMAND_UNAVAILABLE`，不会返回伪造的成功结果。
- Windows 通过现有 loopback 桥使用核心命令授权，命令本体在当前渲染器中相应插件的作用域运行。ZIP 和文件夹选择优先使用官方客户端系统对话框，缺少客户端入口时由同一 Windows 交互桌面中的核心打开系统对话框。也可在安装页手动输入完整路径。
- `GET /enhance/v1/plugins` 根据当前连接返回 `platformLimitations`。安装预览和插件详情返回与该插件声明匹配的部分。每项含 `feature`、`capability` 和中文 `message`。Windows 上主进程桥接入成功后（普通启动会自动接入；兼容模式下需要重开一次完成切换），支持插件隔离网页视图（`browser.view`）、独立插件窗口（`window.popout`）和官方独立右侧 `+` 浮层（`entry.nativeOverlay`）。Windows 工作台或仅接入渲染进程时，这三项仍列为受限，调用返回 `PLATFORM_UNSUPPORTED`。右侧面板标签、会话信息入口、右侧面板列表以及同一文档中的 `+` 菜单可继续挂载。

### v2 输入输出要点

| 动作 | 输入 | 输出 |
|---|---|---|
| `sessions.history` | `{id, beforeOrdinal?, pageSize? (1–100), agentId?}` | `{sessionId, agentId, messages, approvals, hasMore, nextBefore, pageSize}` |
| `sessions.approve` | `{id, interactionId, value}` | 原生 reply 结果 |
| `sessions.abort` | `{id}` | 原生 abort 结果 |
| `sessions.open` | `{id}` | 核心读取并校验会话后返回 `{id}`；宿主再打开官方会话 |
| `sessions.diff` | `{id}` | 读取该会话工作区的 Git 状态与未提交 Diff（经密钥脱敏与长度截断）；无仓库或无变更时返回 `available`/`clean` 与说明 |
| `ui.popout` | `{}` | 核心按令牌返回 `{pluginId}`；调用方不能指定其他插件 |
| `sessions.watch` / `sessions.unwatch` | `{id}` | `{sessionId, watching, forwarded}`；`forwarded` 表示这次是否真的向官方订阅/退订 |
| `attachments.stage` | `{files:[{name, data}]}`，`data` 为 base64 | `{files:[{path, name, size}], batch}`；单文件 ≤ 8 MiB，总量 ≤ 16 MiB，最多 16 个 |
| `attachments.release` | `{batch}` | `{released:true}` |
| `attachments.send` | `{id, text?, paths[], mode?, batch?}` | `{sessionId}`；路径必须是本机绝对路径，最多 16 个。带 `batch` 时无论成功失败都会清理该批次 |
| `browser.open` | `{viewId, url}` | `{pluginId, viewId, url, visible, createdAt}` |
| `browser.navigate` | `{viewId, url}` | 同上 |
| `browser.back` / `browser.forward` / `browser.reload` | `{viewId}` | 授权结果含当前视图归属；宿主执行对应导航，后退/前进不可用时返回 `changed:false` |
| `browser.openExternal` | `{viewId}` | 宿主在系统浏览器打开该视图的当前 `http(s)` 页面，不能由插件另传地址 |
| `browser.bounds` / `browser.visible` | `{viewId, x, y, width, height, visible}` | 同上 |
| `browser.extract` | `{viewId}` | 选中文字或正文，最多 200000 字符 |
| `browser.adapter` | `{viewId, adapter:{source, args?}}` | 授权结果 + `adapter`；宿主在页面里执行 `source` 并返回限长 JSON |
| `browser.close` | `{viewId}` | 同上 + `closed:true` |
| `handoff.preview` | `{targetId?, text, mode?, cwd?, model?, title?}` | `{targetId, mode, text, bytes, newSession}`；新建会话时另含 `cwd`/`model`/`title` |
| `handoff.commit` | `{targetId?, text, mode?, cwd?, model?, title?}` | `{sessionId, created}`；`created` 表示是否新建 |
| `models.list` | `{}` | `{items:[{id,name}], total}` |
| `planner.rename` | `{id, title}` | 持久化后的完整线程；标题去首尾空白后为 1–200 字节，且线程必须属于当前插件 |
| `commands.invoke` | `{command, args?}` | `{command, ownerId, owner, title, args}`（核心授权响应）；`ctx.commands.invoke` 最终返回所属窗口中命令本体的真实结果 |

错误语义：授权失败 `403 PLUGIN_CAPABILITY_DENIED` / `PLUGIN_PRIVILEGE_DENIED`；参数不合法 `400 PLUGIN_PARAMS_INVALID`（`sessions.*` 另用 `PLUGIN_SESSION_REQUIRED`、`PLUGIN_APPROVAL_REQUIRED`、`PLUGIN_HISTORY_PAGE` 指出具体字段；附件用 `PLUGIN_ATTACHMENT_*`，站点适配器用 `PLUGIN_ADAPTER_*`）；资源不存在 `404`；插件未运行 `409`；核心不可用 `503 PLUGIN_CORE_UNAVAILABLE`。所有 `browser.*` 核心动作还需携带窗口实例标识 `instanceId`（经运行时与宿主桥接自动按所在窗口注入，缺省或非法会返回 `400 PLUGIN_BROWSER_VIEW`）。网页视图只接受 `http`/`https`，不跟随凭据 URL，单插件最多 4 个（跨窗口共享配额）。

转发到官方接口后被拒绝的操作（例如不存在的会话）会以 `422 PLUGIN_ACTION_FAILED` 返回，原始原因只记录在核心日志里，不回传给插件；参数本身不合法时在插件门禁处就以 400 明确拒绝，不会走到转发。

### 订阅的持有与释放

`sessions.view` **不会**订阅官方事件流：读取一个会话不该让别人一直保持一条订阅。需要实时更新时必须显式调用 `sessions.watch`，并在不再需要时调用一次 `sessions.unwatch`。

- 订阅按插件计数：同一插件开两个窗格看同一会话，只向官方发一次 `subscribe`，关掉其中一个也不退订。
- 计数在插件之间独立：另一个插件看同一会话时保持自己的计数，互不影响。
- `forwarded:false` 表示这次调用没有触达官方，因为计数没有真正从 0 变 1（或从 1 变 0）。
- 转发失败会回滚本地计数，因此下一次 `watch` 会重新尝试订阅，而不是认为已订阅。`unwatch` 转发失败同样保留本地计数：官方订阅仍开着，插件必须还持有它才能再关一次。
- 同一插件的并发 `watch` 串行执行，第一个订阅被官方拒绝时，同时在等的第二个请求也会得到失败，不会拿到一个并不存在的订阅。
- 核心自身的内部任务（自动化调度、缓存保活、额度恢复）持有的是同一会话的独立内部订阅，插件的引用计数归零不会把它们退订掉，它们按自己的生命周期重连。
- 停用、卸载、故障、安全模式和核心关闭都会释放该插件仍持有的全部订阅。

### 拖入文件与站点适配器

`attachments.pick()` 走系统文件选择器，返回真实路径，可直接交给 `attachments.send`。拖入的文件没有路径，必须经过暂存：

```js
element.addEventListener('drop', async event => {
  const staged = await ctx.attachments.stageFromDrop(event);   // 已 preventDefault
  await ctx.attachments.send({ id: sessionId, text: '看附件', paths: staged.files.map(f => f.path), batch: staged.batch });
});
```

- 字节由渲染层读成 base64 交给核心，核心写入 `<dataDir>/staged/<插件ID>/<批次>/`，不落在插件安装目录，也不会被资源路由当静态文件提供。
- 文件名会清洗（去掉路径分隔符与控制字符、限制长度），写盘路径强制落在批次目录内；`../` 无法逃逸。
- 一个批次只属于一次发送：`attachments.send` 带 `batch` 时，成功与否都会删除该批次；没发送的批次由 30 分钟 TTL 清扫。
- 停用、卸载、故障和安全模式会在停止运行实例时删除该插件的整个暂存根 `<dataDir>/staged/<插件ID>/`。TTL 只在插件再次暂存时才跑，一个已经停用或卸载的插件不会再触发它，所以清理不能只依赖 TTL。
- 清理失败不会被静默忽略：卸载会在活动记录里留下带真实错误的条目，停用与安全模式同样记录，卸载本身仍继续。
- 核心没有数据目录时直接拒绝暂存，不会退回到共享临时目录。

`view.adapter(source, args)` 让站点专用插件在自己的视图里执行一段函数体，用于官方选择器覆盖不到的页面：

```js
const rows = await view.adapter('(limit) => [...document.querySelectorAll("article")].slice(0, limit).map(a => a.innerText)', 20);
```

- `source` 必须是**函数表达式**（箭头函数或 `function`），裸语句会被拒绝；长度上限 32 KiB。
- 执行前会再次向核心确认该视图属于调用方、且当前页面仍为 `http`/`https`；登录重定向和站内路由变化后仍可读取，插件无法用它读别的插件的页面。
- 返回值经 `JSON.stringify` 限长 200000 字符后解析，插件拿到的是数据，不是页面对象。
- 适配器抛错时调用失败，不会退化成返回页面正文；`browser.extract` 的选区/正文回退路径保持不变。

### 交接新会话

`handoff.commit` 不指定 `targetId` 时会新建会话，此时可以指定 `cwd`、`model`、`title`：

- `cwd` 必须是已存在的目录；留空使用核心自己的工作目录。
- `model` 必须在可用模型列表中，并作为第一轮 prompt 的模型下发。
- `title` 最多 120 个字符，留空为「规划交接」。
- 指定了 `targetId` 时这些参数一律被忽略：已有会话不会被换目录、换模型或改标题。

## 运行时接口

```js
export async function activate(ctx) {
  // ctx 见下；返回值可选。返回 { dispose() } 或一个清理函数，都会在停用时调用
}

export function renderSettings(ctx, element) {
  // 可选；element 是插件详情里的设置容器
}
```

```ts
interface PluginContext {
  plugin: { id: string; version: string; dataVersion: number; surface?: "host" | "popout"; platformLimitations?: readonly { feature: string; capability: string; message: string }[] };
  core: { request<T>(action: string, params?: unknown): Promise<T> };
  events: { on(name: string, listener: (payload: unknown) => void): Disposable };
  ui: {
    mount(slot: "sidebar" | "settings", render: (element: HTMLElement) => Disposable | void): Disposable;
    decorate(target: "thinking" | "message", decorator: (block: HTMLElement, info: BlockInfo) => Disposable | DecorationHandle | void): Disposable;
    theme(): { scheme: "light" | "dark"; tokens: Record<string, string> };
  };
  net: { fetch(url: string, init?: ProxyRequestInit & { credential?: string }): Promise<ProxyResponse> };
  storage: {
    get<T>(key: string): Promise<T | undefined>;
    set(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
    list(prefix?: string): Promise<string[]>;
  };
  log(level: "info" | "warn" | "error", message: string): void;
  onDispose(cleanup: () => void): void;

  // v2：以下字段只在授予对应能力时出现，未授权时为 undefined。
  entry?: { add(definition: { id: string; title: string; icon?: string; open(): void | Promise<void> }): Disposable };
  sidebarNav?: { add(definition: { id: string; title: string; icon?: string; open(): void | Promise<void> }): Disposable };
  mainArea?: { mount(definition: { render(el: HTMLElement): Cleanup | void; mode?: "split" | "page"; width?: number; minWidth?: number; maxWidth?: number }): { setWidth(px: number): void; width(): number; dispose(): void } };
  rightPanel?: { mount(definition: { title: string; render(el: HTMLElement): Cleanup | void }): { open(): void; close(): void; readonly open_: boolean; dispose(): void } };
  window?: { popout(): Promise<void>; close(): Promise<void> };
  layout?: { acquireSidebarCollapse(): Disposable };
  sessions?: {
    approve(input): Promise<unknown>;
    abort(id): Promise<unknown>;
    open(id): Promise<void>;
    history(input): Promise<SessionHistoryPage>;
    watch(id): Promise<{ watching: boolean; sessionId: string }>;
    unwatch(id): Promise<{ watching: boolean; sessionId: string }>;
  };
  attachments?: {
    pick(): Promise<Array<{ path: string; name: string }>>;
    /** 暂存拖入的 File；返回的 batch 随 send 一起清理 */
    stage(files: ArrayLike<File>): Promise<{ files: Array<{ path: string; name: string }>; batch: string }>;
    /** 完整的 drop 处理器：已 preventDefault 并暂存 dataTransfer.files */
    stageFromDrop(event: DragEvent): Promise<{ files: Array<{ path: string; name: string }>; batch: string }>;
    release(batch: string): Promise<{ released: boolean }>;
    send(input): Promise<unknown>;
  };
  browser?: { open(viewId: string, url: string): Promise<BrowserView> };
  // BrowserView: navigate(url)、back()、forward()、reload()、openExternal()、
  // setBounds(bounds)、show(bounds)、hide()、extract()、adapter(source, args?)、close()
  planner?: { threads(); models(); create(input); get(id); configure(id, input); rename(id, title); send(id, text, sourceSessionId?); retry(id, revision); stop(id); approve(id, approvalId, allow); remove(id) };
  handoff?: { preview(input): Promise<HandoffPreview>; commit(input): Promise<{ sessionId: string; created?: boolean }> };
  models?: { list(): Promise<Array<{ id: string; name: string }>> };
  commands?: { register(name, input?): Promise<CommandInfo>; unregister(name): Promise<boolean>; list(): Promise<CommandInfo[]>; invoke(name, args?): Promise<unknown> };
}

interface BlockInfo {
  key: string;          // 稳定标识，不要当作正文使用
  role?: string;        // message 目标：assistant / user
  slot: "thinking" | "message";
  open: boolean;        // 思考块是否展开；折叠的块不应读取或翻译
  streaming: boolean;   // 宿主是否仍在追加内容
  revision: number;     // 宿主越改越大；插件自己的节点不计入
}

interface DecorationHandle {
  dispose(): void;
  update?(info: BlockInfo): void;  // 同一元素状态变化时调用，不必重建界面
}
```

装饰的两个约束：

- 插件自己写进宿主的节点必须带 `data-ekdp-owned`。运行时按这个标记区分宿主内容与插件输出；缺标记时插件会把自己的渲染当成宿主更新。
- 状态变化不要改宿主节点的 class（宿主用 class 表达展开/流式）。隐藏原文这类效果用插件自己的属性（例如 `data-ekdp-hide-source`）配合自建样式表实现。

### 凭据动作

```js
const status = await ctx.core.request("credentials.list");
// { items: [{ name, stored, unlocked, usable }], unlocked }
await ctx.core.request("credentials.save", { name: "translator", value: key });
await ctx.core.request("credentials.delete", { name: "translator" });
```

- 三个动作都要求清单里声明了 `credentials:<name>`，具体名字逐个校验，未声明的名字一律拒绝。
- `credentials.save` 只写入、不回读；返回值不含明文，也不含长度。
- `credentials.list` 只在凭据库可用时给出条目状态，不返回任何值。
- 凭据库锁定时写操作返回 `PLUGIN_CREDENTIAL_LOCKED`（HTTP 423）。

约定：

- **不要自己建全局 `MutationObserver`**。`ui.decorate` 由运行时用全宿主唯一的一个 observer 分发，按帧合并扫描。
- 每个 `Disposable` 都登记在插件作用域里；停用插件时运行时逐个释放并移除挂载节点。
- 挂载容器不铺背景，继承宿主 CSS 变量。优先复用宿主已有的结构类名和 `--color-*`、`--font-ui` 变量，不要另起配色体系。
- 插件抛错或 `activate` 失败会被隔离，不影响其他插件和核心。异常超过阈值插件会被自动停用并记录原因。
- `ctx.storage` 单值上限 32 KiB，键名 1–200 字符。
- `ctx.events.on` 的订阅按时长轮询实现，只投递已授权的 scope；未授权的 scope 被丢弃。`planner:<threadId>` scope 只对 `planner:run` 开放。
- v2 的挂载点、网页视图、侧栏引用和命令注册都登记在插件作用域里：停用一个插件只释放它自己的资源，其他插件继续工作。
- 主聊天区的分栏和整页挂载共用一个位置。同一时间只保留一个插件界面。打开新的挂载会先释放前一个界面，关闭已被替换的旧句柄不会影响当前挂载。当前挂载关闭后，官方聊天节点回到原位置。
- 停用时按相反顺序释放：先跑 `activate` 返回的清理和各个 `Disposable`，再移除分栏容器、右面板标签与入口按钮，最后撤销身份令牌。已实测停用后页面与启用前逐项一致（分栏、面板、侧栏折叠、入口都不残留）。
- `ctx.planner.threads()` / `ctx.planner.models()` 是只读发现，不受设置里旧的「规划」附加功能开关影响；该开关只管冻结的内置规划界面。其余 `planner.*` 动作同样走插件自己的授权，不读写那套界面状态。
- 规划线程归属创建它的插件。`planner.configure/send/retry/stop/approve/delete` 只对**同一插件**创建的线程生效；拿其他插件的线程 ID 调用会返回「规划线程不属于该插件」，内置线程（`pluginOwner` 为空）同样如此。`planner.get`/`threads`/`models` 不受此限制，插件仍能看到列表里的每个线程。
- `ctx.entry` 注册的是一个"可打开标签类型"，同一份定义注入三处：会话信息的「可打开」分组（`.sit-available`）、右侧面板的可打开列表（`.pt-body .pl`），以及右侧面板标签栏的 `+` 下拉菜单（当前基线 1.4.2 经主进程 `browser-overlay` IPC 同步并补齐图标）。入口行与官方同行保持一致；`icon` 是画进官方图标槽的单条 SVG path（`viewBox="0 0 24 24"`），不传则保留官方模板图标。
- 1.4.2 里右侧面板主文档列表只有 `.pt-body .pl`（计划里写的 `.pt-body .sit` 和 `.panel-add-menu` 保留兼容分支）；`+` 下拉本身由独立的 `browser-overlay.html` 渲染，由宿主引导脚本在主进程拦截 `kimi:browser-overlay-open` / `kimi:browser-overlay-rendered` / `kimi:browser-overlay-activate` 同步条目与点击调用。1.3.7 及更早版本的同类行为见 `docs/VALIDATION.md`。
- 插件可以在空会话里注册右侧面板。在官方面板出现后，用户可以通过会话信息或右侧面板列表打开插件标签。从会话信息打开时，会话信息按官方逻辑自动隐藏。
- 入口按容器幂等：每处容器最多一行同一个 `action`，官方重渲染后自动补回。运行中启停插件通过核心 `"plugins"` 事件实时推送，三处入口在下一帧内出现或消失，不需要刷新或等待窗口聚焦。
- 官方的「已打开标签页」分组只渲染官方 `panelTabs`，插件标签不在那个数组里，也没有官方 `type` 注册项，因此无法合法注入；插件标签在右侧面板自己的标签栏里显示与关闭。原因记在 `work/correction-20261118/r1-notes.md`。
- `ctx.sidebarNav` 在官方左侧栏「新建会话」下方插入一行，克隆官方 `.btn-new-chat` 行，点击行为由插件决定（通常是打开 `ui:main-area` 页面）。同样按容器幂等、停用即移除。
- 同一个 `viewId` 重复 `browser.open` 只会保留一个网页视图；不同插件即使使用相同 `viewId` 也互不可见。
- 网页视图的归属只由身份令牌决定：请求体里的 `pluginId` 一律忽略，宿主进程用令牌向核心换取权威 `pluginId` 后再操作 Electron 视图。用其他插件的令牌访问自己的 `viewId` 会得到「视图不存在」，与从未创建过一致。
- `view.extract()` 与 `view.adapter()` 操作的是视图**当前所在的页面**，不是 `browser.open` 时登记的地址：登录跳转、站内重定向和 `history.pushState` 之后仍能正常读取。视图被导航到 `file:`、`data:` 或 `javascript:` 时这两个动作会被拒绝。

### 网络与凭据

```js
const response = await ctx.net.fetch("https://api.example.com/v1/chat", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ prompt }),
  credential: "kct/api-key",          // 需要 capabilities 里有 credentials:kct/api-key
  credentialHeader: "Authorization",   // 可选，默认 Authorization
  credentialPrefix: "Bearer ",         // 可选
});
```

- 只允许 `http` 与 `https`；`file:`、`data:`、`javascript:` 一律拒绝。
- 主机必须在清单里逐个列出。URL 不能内嵌凭据。
- 不跟随重定向。3xx 会连同 `redirectTo` 一起返回，需要时由插件显式重试，目标主机同样要过白名单。
- 响应体上限 8 MiB，请求体上限 1 MiB，`Set-Cookie` 会被剥掉。
- 凭据明文只在 Go 侧读取一次并拼进出站请求头；返回值、活动记录和错误文案都不含明文。凭据库锁定时返回 `PLUGIN_CREDENTIAL_LOCKED`（HTTP 423），需要先在插件详情页的凭据区域（或核心凭据接口）解锁并配置。

## 生命周期

| 状态 | 含义 |
|---|---|
| `DISABLED` | 已安装、未启用。 |
| `ENABLED` | 正在运行，持有该 App 实例签发的身份令牌。 |
| `BLOCKED` | 被安全模式停止。 |
| `INCOMPATIBLE` | 宿主版本不在 `hostVersions` 内。 |
| `CORRUPT` | 清单或入口文件校验失败。文件保留不删，便于修正。 |
| `FAULTED` | 运行期异常被自动停用。 |

- 安装后插件**保持停用**，确认能力后再启用。
- 停用、卸载、安全模式、核心关闭都会立即撤销身份令牌。
- 卸载会删除插件文件与私有存储，并写下墓碑记录：**预装插件被卸载后不会自动恢复**（可通过 `restore` 接口显式恢复）。
- 预装插件在首次运行时释放到 `<dataDir>/plugins/<id>/`；已存在且未卸载的目录在版本相同或更新时保持不变（手工修改不会被每次启动冲掉），当二进制内嵌版本严格高于已安装版本时会自动平滑升级文件，同时保留原有授权、启用意图和私有存储。
- 插件异常（清单损坏、入口缺失）会被标记为 `CORRUPT` 并记录活动；**不会删除用户文件**。

## HTTP 接口

插件运行时使用，由宿主桥以 `core.fetch` 转发：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/enhance/v1/plugin-runtime/{action}` | 插件动作。body `{token, params}`；**由令牌决定插件身份**，URL 不能命名插件。 |
| GET | `/enhance/v1/plugins/{id}/assets/{file}` | 加载插件自己的 ES module（`entry.renderer`、`entry.settings`）及 `assets/` 目录下的静态文件。只服务已启用插件，拒绝路径穿越与符号链接。 |
| POST | `/enhance/v1/plugins/{id}/token` | 取得该插件的身份令牌（同一令牌），供窗口打开前已启用的插件使用。 |

管理界面使用：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/enhance/v1/plugins` | 列表，含 `safeMode`、`hostVersion`、`apiVersion`、结构化上游信息与 `hasNotice`。 |
| GET | `/enhance/v1/plugins/health` | 汇总。 |
| GET | `/enhance/v1/plugins/{id}` | 单个插件详情。 |
| GET | `/enhance/v1/plugins/{id}/legal` | 读取插件目录内的 `LICENSE` 与 `NOTICE`/`NOTICE.md` 及上游元数据；插件停用时也可读取。 |
| POST | `/enhance/v1/plugins/preview` | body `{root}`，校验本地目录或 zip，返回清单、上游、高权限能力和暂存内容的 `packageDigest`，不安装插件。 |
| POST | `/enhance/v1/plugins/install` | body `{root, packageDigest, capabilities, confirmPrivileged?}`，安装本地文件夹或 zip。`packageDigest` 必须来自该包的预览。包改变时返回 `409 PLUGIN_PACKAGE_CHANGED`，需重新预览并确认授权。授予高权限能力时需传 `confirmPrivileged: true`。 |
| POST | `/enhance/v1/plugins/{id}/enable` | 启用，响应含 `token`。 |
| POST | `/enhance/v1/plugins/{id}/disable` | 停用并撤销令牌。 |
| POST | `/enhance/v1/plugins/{id}/uninstall` | 卸载并写墓碑。 |
| POST | `/enhance/v1/plugins/{id}/restore` | 清除用户卸载留下的墓碑并重新释放二进制内嵌副本，用于「恢复预装插件」。已安装时返回 `409 PLUGIN_ALREADY_INSTALLED`；这不是预装插件时返回 `404 PLUGIN_NOT_BUNDLED`。恢复后仍是停用状态，需再调用 enable。 |
| POST | `/enhance/v1/plugins/{id}/force-enable` | body 必须为 `{confirm: true}`，否则 `400 PLUGIN_FORCE_CONFIRM_REQUIRED`。仅用于绕过宿主编译版本不在 `hostVersions` 内的门禁（`409 PLUGIN_INCOMPATIBLE`）：用户确认风险后强制启用，不是故障恢复手段。 |
| GET | `/enhance/v1/plugins/updates` | 官方预装插件在线更新快照：`state`（`idle`/`checking`/`ready`/`error`）、`checkedAt`、`source`、`availableCount` 与按插件 ID 索引的 `items`（当前/最新版本、`updateAvailable`、`canApply`、`cannotApplyReason`、`needsConsent`、`addedCapabilities`、`addedPrivileged`、`minCoreVersion`、`hostCompatible`、`publishedAt`、`releaseNotes`）。 |
| POST | `/enhance/v1/plugins/updates/check` | body 可省略；重新从 `plugins-latest` 通道拉取并验签清单，按 CNB 优先、GitHub 兜底换源。返回同一份 `UpdateStatus`。 |
| POST | `/enhance/v1/plugins/{id}/update` | body `{expectedVersion?, confirmCapabilities?, confirmPrivileged?}`。校验内置公钥签名、`minCoreVersion` 与能力差异后热替换插件文件；能力扩权需 `confirmCapabilities: true`，新增高权限能力还需 `confirmPrivileged: true`。`expectedVersion` 与最新版本不符时拒绝，避免装进更新的包。 |
| GET | `/enhance/v1/plugins/{id}/activity` | 活动记录，分页，已脱敏。 |
| GET | `/enhance/v1/kct/migration` | 独立版 KCT 的迁移状态，只含公共设置，按 `plugins:read` 授权。 |
| POST | `/enhance/v1/kct/migration/confirm` | 用户确认后停用独立版加载器并迁移公共设置，按 `plugins:write` 授权。 |

## 官方预装插件独立在线更新

从 `v0.2.8` 起，四个预装插件（`kimicode-plus.monitor`、`kimicode-plus.kct`、`kimicode-plus.planner`、`kimicode-plus.scheduler`）可以脱离主程序单独发版并热更新，不重装核心、不重启 Kimi Code。

### 通道与来源

清单固定从非 latest 的滚动通道标签 `plugins-latest` 读取，与主程序的 `releases/latest` 互不影响：

```text
https://cnb.cool/wssz527/kimicode-plus/-/releases/download/plugins-latest/plugins-manifest.json
https://github.com/wssz527/kimicode-plus/releases/download/plugins-latest/plugins-manifest.json
```

客户端按 CNB 国内镜像优先、GitHub 兜底换源，单源检查超时 8 秒、单包下载超时 60 秒。`releaseTag` 可由 `UpdateOptions.ReleaseTag` 覆盖，仓库与环境变量规则同 `internal/plugins/update.go`。

### 清单与签名

`plugins-manifest.json` 是单文件原子信封，签名覆盖域分隔串加清单原始字节：

```json
{
  "format": "kimicode-plus.plugins-manifest.v1",
  "keyId": "official-ed25519-2026",
  "signature": "<base64 Ed25519 签名>",
  "manifest": { "generatedAt": "...", "plugins": [ ... ] }
}
```

- 签名内容是 `"kimicode-plus-plugins-manifest-v1\n"` 与 `manifest` 字段的**原始 UTF-8 字节**拼接，不做二次序列化，避免键序造成的验签漂移。
- 公钥内置在 `plugins/official_keys.go`（`OfficialSigningPublicKeyBase64` → `OfficialPublicKeys()`），私钥只存在于仓库 Secret，不提交。清单上限 2 MiB，`contentDigest`/`archiveSha256` 必须是 64 位小写十六进制。
- 每个条目含 `id`、`version`、`apiVersion`、`hostVersions`、`minCoreVersion`、`capabilities`、`archive`（`*.zip` 文件名）、`archiveBytes`、`archiveSha256`、`contentDigest`、`publishedAt`、`releaseNotes`。
- 验签失败、格式不符或超出上限时整份清单作废，不会退化为「只信任其中几条」。

### 检查与应用的判定顺序

1. **验签**：未通过内置公钥校验的清单直接拒绝，不比较版本。
2. **最低核心版本**：清单条目的 `minCoreVersion` 高于当前核心版本时标记不可应用（`cannotApplyReason`），绝不为装插件而回退客户端。
3. **宿主兼容**：`hostVersions` 不含当前官方客户端版本时标记 `hostCompatible: false`。
4. **能力差异**：与已安装版本比较能力清单。新增能力置 `needsConsent: true`，客户端必须带 `confirmCapabilities: true`；其中新增高权限能力还需 `confirmPrivileged: true`。能力集合未扩张时可直接热更新。
5. **版本锁定**：应用请求里的 `expectedVersion` 与最新版本不一致时拒绝，避免在用户确认后装进另一个更新的包。

热替换把版本化 `*.zip` 下载到本地并校验 `archiveSha256` 与目录内容摘要，替换 `plugins/<id>` 后按插件记录重新落盘；已验签清单随插件记录持久化，重启后仍用于高权限复核（见前文 `signedOfficialDigest`）。

### 发布流程

```sh
go run ./cmd/plugin-release package --out dist/plugins-release \
  --key-env PLUGIN_SIGNING_PRIVATE_KEY --min-core 0.2.8 \
  --published-at <ISO8601> --notes <更新说明>
go run ./cmd/plugin-release verify --dir dist/plugins-release
```

`package` 按 `plugins/embed.go` 的内嵌集合打包并签名，`verify` 用内置公钥反向校验签名、每个压缩包的 SHA-256 与目录内容摘要。`.github/workflows/release-plugins.yml` 在推送 `plugin-*` 标签或手动触发时执行上述两步，先上传各版本化 `*.zip`、最后原子覆盖 `plugins-manifest.json` 到 GitHub `plugins-latest`，再同步到 CNB 镜像。发布前还会校验 `plugins/monitor/` 与 `plugins/monitor-src/` 打包结果一致，避免把旧 `renderer.js` 签出去。

## 安装包校验

- zip：条目数 ≤ 64，单文件 ≤ 4 MiB，解压总量 ≤ 16 MiB。
- 拒绝绝对路径、`..` 越界、符号链接条目（zip slip）。
- 文件夹安装同样拒绝符号链接和非普通文件。
- 先解包到临时目录并完整校验，通过后才替换 `<dataDir>/plugins/<id>`：现有目录先重命名为 `<id>.previous`，新目录就位后删除备份；新目录就位失败时把备份改回原名，原插件保留。替换期间插件先被停用，因此不存在新旧文件同时被加载的窗口。

## 活动记录

以下动作会写入活动记录（活动记录中的凭据明文由核心脱敏）：

- 安装、启用、停用、卸载
- 异常停用、版本门禁拒绝、安全模式停止
- 向会话发送消息、修改 Kimi Code 配置
- 经代理发出的网络请求（方法 + 主机 + 状态码）
- 凭据的保存与删除（只记名字，不记值）
- 插件自己通过 `ctx.log` 输出的日志

## 预装适配插件

### Monitor（`kimicode-plus.monitor`）

`plugins/monitor/` 按上游 `bfjnbvf/kimi-code-monitor@1d529f0b` 桌面客户端版复刻界面、Rive 蓝球形象与交互（构建源码位于 `plugins/monitor-src/`），数据全部来自核心插件接口；核心不再有任何内置 Monitor 界面或私有动作。

| 项 | 实际内容 |
|---|---|
| 能力 | `metrics:read`、`ui:sidebar`、`ui:settings`、`storage`。**不申请** `sessions:read`：它只读指标与生命周期状态，不读会话正文。 |
| 数据 | `cache.summary`、`cache.history`（24h/7d/30d，含主/子代理分桶）、`cache.turns`（本会话逐轮 step 去重统计与全宽折线）、`quota.balances`（Kimi Code 5h/每周额度、加油包与官方已配置供应商的外部余额），以及 `activity` 作用域的回合/工具状态。 |
| 未知与容错 | `NOT_REPORTED`、`null`、`UNKNOWN` 显示为 `--`，读取失败绝不产生 `0`，外部余额失败时仅沿用同一身份上一次有效读数并标注状态；所有供应商密钥仅在 Go 端用于请求头，不进入渲染进程、存储或日志。 |
| 模块与交互 | 长按进入三区编辑（隐藏 / 展开 / 固定区）、区内拖拽排序与半宽/全宽混排、模块 `≡` 菜单、迷你模式与可选侧栏整理（默认关闭），全部经 `ctx.storage` 持久化。余额模块仅在可见时按 60 秒轮询，收起即停。 |
| 版权与声明 | `plugins/monitor/LICENSE` 与 `NOTICE` 随包交付上游 MIT 全文、Rive 官方形象说明及 Kimi Work+ 余额模块来源，并纳入 `scripts/notices.py`。 |

停用 Monitor 后，核心「缓存与用量」页的数据不受影响，侧栏恢复为纯官方样式、零残留节点与定时器。

### 定时任务（`kimicode-plus.scheduler`）

`plugins/scheduler/` 为预装、默认关闭的官方 v2 功能插件，调度引擎持久化在核心 `internal/scheduler`。

- 入口与页面：`ui:sidebar-nav` 在左侧栏“新建会话”下方注入“定时任务”，点击在主聊天区（`ui:main-area` `mode: "page"`）打开任务与运行记录页；`ui:entry` 在会话信息与右侧面板提供当前会话绑定的任务视图。
- 调度与安全：支持单次、预设、每日多时刻和自定义间隔/周几，错过运行默认合并补跑一次（可改为跳过）；支持新开会话与绑定会话投递；无人值守出现审批请求时不自动批准，标记为“等待批准”并保留未读提醒；卸载时可选择保留或一并删除已创建的任务数据。

## 示例与第三方快速起步

### 基础示例（v1：`plugins/example-hello/`）

`plugins/example-hello/` 不再预装在发布二进制中，只作为第三方开发模板保留在仓库里。它包含最简结构：侧栏一行文字 + 一个设置子页 + 使用私有存储记录启用次数，可直接复制改名开发自己的插件。

如需在 KimiCode+ 中加载它，打开官方设置「插件 → KimiCode+」分段，点击「安装本地增强插件」，再点「选择文件夹」选中 `plugins/example-hello/`（或打成 `.zip` 后选包），在权限确认弹层中核对 `ui:sidebar`、`ui:settings`、`storage` 后完成安装，再手动启用。

### 扩展挂载示例（v2：左侧栏导航 + 主区页面 + 右侧面板）

第三方插件声明 `"apiVersion": 2` 后，可以直接使用全部开放挂载点：

```json
{
  "id": "yourname.my-workbench",
  "name": "我的工作台",
  "version": "1.0.0",
  "apiVersion": 2,
  "description": "在左侧栏增加一级入口，并在主区和右侧面板打开自定义工具。",
  "author": "yourname",
  "license": "MIT",
  "hostVersions": ["1.*"],
  "entry": { "renderer": "renderer.js" },
  "capabilities": ["ui:sidebar-nav", "ui:main-area", "ui:right-panel", "ui:entry", "storage"]
}
```

```js
export async function activate(ctx) {
  let pageHandle = null;

  // 1. 在左侧栏「新建会话」下方挂一级导航，点击打开主区整页
  const nav = ctx.sidebarNav?.add({
    id: "workbench",
    title: "我的工作台",
    open() {
      pageHandle?.dispose();
      pageHandle = ctx.mainArea?.mount({
        mode: "page",
        render(el) {
          el.innerHTML = `<div style="padding:24px"><h2>我的工作台</h2><p>第三方 v2 插件主区页面已挂载。</p></div>`;
          return () => { el.textContent = ""; };
        },
      }) ?? null;
    },
  });

  // 2. 在右侧面板挂独立标签页，并在会话信息 / 右侧 + 菜单添加入口
  const panel = ctx.rightPanel?.mount({
    title: "快捷工具",
    render(el) {
      el.innerHTML = `<div style="padding:16px">右侧面板内容</div>`;
      return () => { el.textContent = ""; };
    },
  });
  const entry = ctx.entry?.add({
    id: "quick-tool",
    title: "快捷工具",
    open: () => panel?.open(),
  });

  ctx.onDispose(() => {
    entry?.dispose();
    panel?.dispose();
    pageHandle?.dispose();
    nav?.dispose();
  });
}
```

安装时在权限确认界面勾选对应能力并确认高权限风险提示，启用即可生效。

### 预装适配示例（`plugins/kct/`）

`plugins/kct/` 是完整 v1 适配示例（移植自 MIT 许可的 `https://github.com/wssz527/kimi-cot-translate`）：装饰思考块、分段与码块保护、三种翻译服务、`credentials:` 注入与私有存储缓存。它同时演示了「插件被宿主门禁拦住」的场景：独立版 KCT 的后台加载器仍在时，它的启用会被核心以 `PLUGIN_MIGRATION_REQUIRED` 拒绝。

构建与验证：

```bash
go build ./...            # 核心与预装插件嵌入
go test ./internal/plugins/...
cd web && npm run typecheck && npm test
```
