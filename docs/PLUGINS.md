# 插件开发说明（插件系统 v1 / v2）

> 版本关系：`apiVersion: 1` 的清单与运行时接口保持原样，Monitor、KCT 和示例插件不需要改动。`apiVersion: 2` 增加一组高权限能力，**只授予内容摘要匹配的官方预装插件**；第三方插件即使声明也会在安装阶段被拒绝。下文能力清单按 v1 / v2 分节。

KimiCode+ 支持本地受信插件。插件是运行在宿主渲染进程里的 ES module，可以贴合官方界面，并通过核心提供的接口取用缓存指标、会话、配置和网络。核心在 Go 端按插件身份与能力强制授权，渲染层的校验只是第二道防线。

本轮范围：

- 只从**本地文件夹或 zip** 安装，没有插件市场、在线安装源或签名校验。
- 不做不可信插件沙箱。插件运行在宿主渲染进程里，**可以读取当前界面内容**，界面会如实告知这一点。
- Monitor 与 KCT 都只是这套接口下的适配插件，核心没有为它们留私有通道。核心关闭全部插件后仍完整可用。

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
| `hostVersions` | 1–32 项。写具体版本（`1.3.5`）或点边界通配（`1.*`、`1.3.*`）。**指官方 Kimi Code 客户端版本**，不是 KimiCode+ 的版本。 |
| `entry.renderer` | 必需，相对路径，`.js`，必须位于插件目录内。 |
| `entry.settings` | 可选，同上。 |
| `capabilities` | 0–64 项，见下节。未知名称、重复项、空项都会被拒绝。 |
| `LICENSE` | 文件必须存在。 |

清单解析是严格的：未知字段、对象后的多余内容、超过 64 KiB 都会被拒绝。这样写错一个能力名会立刻失败，而不是被静默忽略后少授权。

**版本门禁**：宿主客户端版本不在 `hostVersions` 内时，插件保持停用并显示"未在此版本验证"。宿主版本未知时（例如尚未接入官方客户端）不做拦截。

## 能力清单 v1

| 能力 | 提供内容 |
|---|---|
| `metrics:read` | `cache.summary`、`cache.sessions`、`cache.history`、`cache.retention`、`cache.receipts`，以及 `cache`/`automation` 事件订阅。 |
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

## 能力清单 v2（高权限，仅官方预装）

v2 能力由构建时机制授权：`plugins/registry.go` 对每个嵌入插件目录计算 SHA-256（排序后的相对路径 + 字节长度 + 字节内容），写入二进制内的允许列表。启用和**每一次**高权限调用都会重新哈希安装目录，摘要不一致立即返回 `403 PLUGIN_PRIVILEGE_DENIED`。这不是密钥签名，也不保护已被替换的二进制；它保证的是「本地安装的插件冒充同名官方插件」和「官方插件文件被改写」都会被挡住。

| 能力 | 提供内容 | 对应动作 |
|---|---|---|
| `ui:entry` | 右侧「可打开」列表和 `+` 菜单入口；按容器幂等注入 | 运行时 `ctx.entry.add()`，无独立动作 |
| `ui:main-area` | 主聊天区右侧分栏，可拖动分隔条调整原生视图宽度 | 运行时 `ctx.mainArea.mount()` |
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
| `models:read` | 可用模型列表 | `models.list` |
| `commands` | 插件命令注册与跨插件调用 | `commands.register/unregister/list/invoke` |

约束：

- 第三方（本地文件夹或 zip）安装时提交这些能力会返回 `403 PLUGIN_CAPABILITY_PRIVILEGED`，不会写入授权记录。
- 摘要不匹配时，`runtime.info` 不再列出高权限能力，运行时因此不会渲染对应界面；即使插件绕过界面直接调用，Go 门禁也会拒绝。
- 停用、卸载、故障、安全模式和核心关闭都会撤销身份令牌，并同时释放该插件的网页视图、命令注册与会话订阅；这些停止路径都会结束该插件创建且仍在运行的规划线程（历史保留）。
- `commands` 只做身份与归属校验，命令本体在插件自己的渲染上下文里执行。调用不存在的命令返回 `404 PLUGIN_COMMAND_UNKNOWN`，所属插件未运行返回 `409 PLUGIN_COMMAND_UNAVAILABLE`，不会返回伪造的成功结果。

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
  plugin: { id: string; version: string; dataVersion: number };
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
  entry?: { add(definition: { id: string; label: string; run(): void | Promise<void> }): Disposable };
  mainArea?: { mount(definition: { render(el: HTMLElement): Cleanup | void; width?: number; minWidth?: number; maxWidth?: number }): { setWidth(px: number): void; width(): number; dispose(): void } };
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
- 停用时按相反顺序释放：先跑 `activate` 返回的清理和各个 `Disposable`，再移除分栏容器、右面板标签与入口按钮，最后撤销身份令牌。已实测停用后页面与启用前逐项一致（分栏、面板、侧栏折叠、入口都不残留）。
- `ctx.planner.threads()` / `ctx.planner.models()` 是只读发现，不受设置里旧的「规划」附加功能开关影响；该开关只管冻结的内置规划界面。其余 `planner.*` 动作同样走插件自己的授权，不读写那套界面状态。
- 规划线程归属创建它的插件。`planner.configure/send/retry/stop/approve/delete` 只对**同一插件**创建的线程生效；拿其他插件的线程 ID 调用会返回「规划线程不属于该插件」，内置线程（`pluginOwner` 为空）同样如此。`planner.get`/`threads`/`models` 不受此限制，插件仍能看到列表里的每个线程。
- `ctx.entry` 的入口同时出现在右侧列表和 `+` 菜单里，与原生「侧边聊天」一致：没有会话时 `+` 菜单整组不可用，入口按钮也随之为 `disabled`，这是宿主行为而非插件失败。
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
- 凭据明文只在 Go 侧读取一次并拼进出站请求头；返回值、活动记录和错误文案都不含明文。凭据库锁定时返回 `PLUGIN_CREDENTIAL_LOCKED`（HTTP 423），需要先在凭据页解锁。

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
- 卸载会删除插件文件与私有存储，并写下墓碑记录：**预装插件被卸载后不会自动恢复**。
- 预装插件在首次运行或升级时释放到 `<dataDir>/plugins/<id>/`，已存在且未卸载的不会被覆盖，所以手工修改不会被冲掉。
- 插件异常（清单损坏、入口缺失）会被标记为 `CORRUPT` 并记录活动；**不会删除用户文件**。

## HTTP 接口

插件运行时使用，由宿主桥以 `core.fetch` 转发：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/enhance/v1/plugin-runtime/{action}` | 插件动作。body `{token, params}`；**由令牌决定插件身份**，URL 不能命名插件。 |
| GET | `/enhance/v1/plugins/{id}/assets/{file}` | 加载插件自己的 ES module。只服务已启用插件、只允许插件目录内文件。 |
| POST | `/enhance/v1/plugins/{id}/token` | 取得该插件的身份令牌（同一令牌），供窗口打开前已启用的插件使用。 |

管理界面使用：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/enhance/v1/plugins` | 列表，含 `safeMode`、`hostVersion`、`apiVersion`。 |
| GET | `/enhance/v1/plugins/health` | 汇总。 |
| GET | `/enhance/v1/plugins/{id}` | 单个插件。 |
| POST | `/enhance/v1/plugins/install` | body `{root, capabilities?}`，安装本地文件夹或 zip。 |
| POST | `/enhance/v1/plugins/{id}/enable` | 启用，响应含 `token`。 |
| POST | `/enhance/v1/plugins/{id}/disable` | 停用并撤销令牌。 |
| POST | `/enhance/v1/plugins/{id}/uninstall` | 卸载并写墓碑。 |
| GET | `/enhance/v1/plugins/{id}/activity` | 活动记录，分页，已脱敏。 |
| GET | `/enhance/v1/kct/migration` | 独立版 KCT 的迁移状态，只含公共设置，按 `plugins:read` 授权。 |
| POST | `/enhance/v1/kct/migration/confirm` | 用户确认后停用独立版加载器并迁移公共设置，按 `plugins:write` 授权。 |

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

`plugins/monitor/` 是核心侧栏缓存组件的正式实现；核心不再有任何 Monitor 界面或私有动作（计划 D5、D10）。

| 项 | 实际内容 |
|---|---|
| 能力 | `metrics:read`、`ui:sidebar`、`ui:settings`、`storage`。**不申请** `sessions:read`：它只读指标，不读会话正文。 |
| 数据 | 仅 `ctx.core.request`：`cache.summary`（今日/近 1 小时、按模型、速度、额度）、`cache.sessions`、`cache.history`（24h/7d/30d）、`cache.receipts`。与设置里的“缓存与额度”页同一时刻同源。 |
| 未知 | `NOT_REPORTED`、`null`、`UNKNOWN` 一律显示为 `--`／“未上报”，从不补零；额度按 `quota.value.quota.usages` 读 `usedRatio`，实际 0% 仍是 0%。 |
| 子代理 | `cache.sessions` 只提供会话级的 `openBackground`（未返回的后台子代理数）与 `blocked`。插件按会话列出等待中的计数，并明确说明拿不到逐个代理的名称；不虚构代理行或 token。 |
| 刷新 | 订阅 `cache`／`automation` 事件触发，10 秒合并窗口 + 60 秒兜底；手动刷新会开新一代，旧响应不会覆盖新结果。 |
| 设置 | 默认迷你、是否显示 Kimi 形象、默认统计窗口；全部经 `ctx.storage` 持久化，重启后恢复。侧栏与设置子页通过同一个 `ctx` 上的命名空间通道同步，无需重新启用插件。 |
| 样式 | 单文件内的 `<style>` 注入挂载元素的 shadow root，容器完全透明，颜色全部取自宿主 `--color-*`/`--ink*` 变量，`color-mix` 派生次级色；无网络请求、无全局 observer、无私有存储。 |

`plugins/monitor/LICENSE` 随包交付上游 `bfjnbvf/kimi-code-monitor@1d529f0b` 的 MIT 全文与归属；`plugin.json` 的 `upstream` 字段与 `scripts/notices.py` 使用同一来源。Kimi 形象是插件自绘的几何图形，不使用官方商标素材，也不联网下载。

停用 Monitor 后，核心的“缓存与额度”页数据不受影响，侧栏恢复为纯官方样式：插件只拥有自己的挂载节点，不由核心代管。

## 示例

`plugins/example-hello/` 不再预装在发布二进制中，只作为第三方开发模板保留在仓库里。它包含最简结构：侧栏一行文字 + 一个设置子页 + 使用私有存储记录启用次数，可直接复制改名开发自己的插件。

如需在 KimiCode+ 中加载它，打开设置里的「插件 → 安装本地插件」，选择 `plugins/example-hello/` 目录（或先打成 `.zip`），在权限确认弹层中核对 `ui:sidebar`、`ui:settings`、`storage` 后完成安装，再手动启用。已安装过旧版示例插件的用户在升级后不会被自动卸载；卸载后「恢复预装插件」不会再恢复它。

`plugins/kct/` 是完整示例（移植自 MIT 许可的 `kimi-cot-translate`）：装饰思考块、分段与码块保护、三种翻译服务、`credentials:` 注入与私有存储缓存。它同时演示了「插件被宿主门禁拦住」的场景：独立版 KCT 的后台加载器仍在时，它的启用会被核心以 `PLUGIN_MIGRATION_REQUIRED` 拒绝。

构建与验证：

```bash
go build ./...            # 核心与预装插件嵌入
go test ./internal/plugins/...
cd web && npm run typecheck && npm test
```
