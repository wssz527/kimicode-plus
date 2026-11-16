# 插件开发说明（插件系统 v1）

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
| `apiVersion` | 必须为 `1`。其他版本直接拒绝，不做猜测。 |
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

## 运行时接口

```js
export async function activate(ctx) {
  // ctx 见下；返回值可选，返回 { dispose() } 会在停用时调用
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
- `ctx.events.on` 的订阅按时长轮询实现，只投递已授权的 scope；未授权的 scope 被丢弃。

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

## 示例

`plugins/example-hello/` 是本仓库自带的最小示例：侧栏一行文字 + 一个设置子页 + 使用私有存储记录启用次数。它也是第三方开发者的起点，可以直接复制改名。

`plugins/kct/` 是完整示例（移植自 MIT 许可的 `kimi-cot-translate`）：装饰思考块、分段与码块保护、三种翻译服务、`credentials:` 注入与私有存储缓存。它同时演示了「插件被宿主门禁拦住」的场景：独立版 KCT 的后台加载器仍在时，它的启用会被核心以 `PLUGIN_MIGRATION_REQUIRED` 拒绝。

构建与验证：

```bash
go build ./...            # 核心与预装插件嵌入
go test ./internal/plugins/...
cd web && npm run typecheck && npm test
```
