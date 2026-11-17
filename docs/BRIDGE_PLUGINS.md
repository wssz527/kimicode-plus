# 任务桥接、模拟适配器与插件生命周期

本文件记录旧版 `internal/bridge` Outbox、Mock Adapter、`internal/plugins` 与 `internal/redact` 的实现契约、与 `docs/API_CONTRACT.md` 的精确偏差，以及可复现的验证结果。`internal/desktop` 现已另有真实 KAP 会话、规划、运行时接入、后台自动化和额度恢复路径；本机附件也可直接经 KAP prompt 发送，它们不经过本文的 Mock Outbox，详见 `NATIVE_INTEGRATION.md`。缓存指标的官方额度读取和 UNKNOWN 处理属于原生路径，不能与本文件的兼容事件导入或模拟资源计数混为一谈。侧栏 Monitor 现在是预装插件（`plugins/monitor/`），只经 `metrics:read` 读 `cache.*`；核心没有 `monitor.summary` 私有通道。界面参考固定 MIT 上游版本，许可随插件包交付，但不运行上游采集器；自动化回执不代表缓存命中率。

## 模块位置与集成签名

| 模块 | 文件 | 构造函数 / 入口 | 挂载 |
|---|---|---|---|
| 脱敏 | `internal/redact/redact.go` | `Text(string) string`、`Findings(string) []string`、`JoinFindings(...[]string) []string` | 无路由 |
| 模拟适配器 | `internal/desktop/mock.go` | `NewMock() *Mock`；`Mock` 实现 `Adapter` | `Register(mux)` |
| 任务桥 | `internal/bridge/model.go`、`internal/bridge/service.go` | `New(db *sql.DB, adapter desktop.Adapter, safe func() bool) (*Service, error)` | `Register(mux)` |
| 插件管理 | `internal/plugins/manager.go` | `New(db *sql.DB, safe func() bool) (*Manager, error)` | `Register(mux)`、`Close() error` |

所有 `Register` 只注册 `/enhance/v1` 开头的完整模式，自身不创建子路由，也不假定挂载前缀。`bridge.New` 与 `plugins.New` 各自调用 `storage.Migrate`，模块名分别为 `bridge`、`plugins`；迁移在同一事务内记录版本，模块版本高于程序时报错而不是继续运行。

`desktop.Adapter`：

```go
type Adapter interface {
    Status(ctx context.Context) Status
    Sessions(ctx context.Context) ([]Session, error)
    Submit(ctx context.Context, submission Submission) (Receipt, error)
    Reconcile(ctx context.Context, request ReconcileRequest) (Receipt, bool, error)
}
```

`Reconcile` 的布尔值表示"找到匹配回执"，`false` 且 `nil` 错误表示适配器确认无回执；错误表示查询本身失败。两者都不触发重投。若扩展旧 Outbox 的真实传输，可以实现该接口；当前原生 `handoff` 已另走 KAP，不经过此 Mock Adapter。

## HTTP 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/enhance/v1/adapter` | `desktop.Status` |
| PUT | `/enhance/v1/adapter` | `{"mode":"AVAILABLE"\|"CHANGED"\|"UNAVAILABLE"}`，返回新 `Status` |
| GET | `/enhance/v1/sessions` | 合成引用列表，`{items,total,mock,ownership,notice}`，支持 `offset`/`limit` |
| GET | `/enhance/v1/tasks` | Outbox 列表 `{items,total}`，按 `createdAt` 倒序；`COUNT(1)` + `LIMIT/OFFSET`，只解析当前页 |
| POST | `/enhance/v1/tasks/preview` | `{task}` => `PreviewResult` |
| POST | `/enhance/v1/tasks` | `{intent,task,confirmed,confirmationHash?,mockBehavior?,acknowledgeUnverifiedAttachments?}` => `Record` |
| POST | `/enhance/v1/tasks/{id}/reconcile` | 请求体必须是 `{}`；只查询，不提交 |
| GET | `/enhance/v1/plugins` | 内置模拟插件列表 + `safeMode` + `notice` |
| GET | `/enhance/v1/plugins/health` | 状态/资源/故障计数聚合，`processes` 恒为 0 |
| POST | `/enhance/v1/plugins/{id}` | `{"action":"ENABLE"\|"DISABLE"\|"FAULT"}` |

错误统一为 `{error:{code,message}}`。插件动作失败时，响应同时带 `plugin` 字段返回故障后的快照，便于界面就地更新。

## 任务包与校验

`TaskPackage` 字段与契约一致：`version`、`taskId`、`direction`、`sessionId`、`projectId`、`source{site,url,threadId,messageIds}`、`goal`、`messages[]`、`diff`、`attachments[]`、`checkpoint`、`parentCheckpoint`、`parentHash`、`linkedTaskId`、`mode`。消息额外带只读的 `sequence`，服务端强制把 `trust` 覆写为 `untrusted-web`。

规范化为 JSON 后的整体 SHA-256 即 **confirmation hash**。预览返回同一个哈希；提交时若提供 `confirmationHash` 且与当前内容不符，返回 `409 CONFIRMATION_STALE`，因此确认后的 body 不能被替换。

已实现的强校验：

- `version` 必须为 1；`direction` 只能是 `CHAT_TO_CODE`、`CODE_TO_CHAT`、`CHAT_TO_CODE_CONTINUE`；`mode` 只能是 `PLAN`、`EXECUTE`、`TOWER`。
- `source.url` 必须是完整 HTTPS 地址。桥接**不访问**该地址，`sourceUrl` 只作为引用保存。
- Chat→Code 方向必须给出所选消息，且 `messages[].id` 必须落在 `source.messageIds` 内（`SOURCE_SELECTION_MISMATCH`），防止把未选择的消息夹带出去。
- 消息 ID 唯一，`role` 限 `user`/`assistant`/`system`；单条 100 000 字节，总消息数 500，diff 500 000 字节，goal 20 000 字节。
- 附件通过 `POST /attachments` 独立有界二进制上传（兼容 legacy dataBase64），必须匹配大小和 SHA-256；单项 1 MiB，私有 SQLite store 总量 64 MiB，零字节明确拒绝。不读取任意路径、不下载 URL。无字节引用保持 UNAVAILABLE，需要明确确认缺口。
- 核心持久原始字节，任务/预览只保存稳定 blob 引用，不回显字节。Outbox claim 同事务绑定附件权限；transport 只获得本任务的受限读取器。STORED 表示本地保存，逐附件有效目标回执才标 UPLOADED。没有引用的 blob 24 小时后可回收，历史引用保留。扫描范围有限，文本脱敏不能保证附件安全。
- 派发前校验实际 Session project/host；Web 方向另绑定 site/origin/tab/thread，使用独立 WebTransport。安全模式与真正调用共享许可门，尚未进入的调用被阻止，已经发送的调用不能假装撤销。此旧路径的 Code/Web Mock 回执分别标记 MOCK_CODE / MOCK_WEB；真实 KAP 会话与交接契约另见 `NATIVE_INTEGRATION.md`。

## 脱敏

`redact.Text` 按固定顺序应用规则，覆盖多行 PEM（含 OpenSSH、PGP、`BEGIN ... PRIVATE KEY BLOCK`、缺少 END 的截断块）、Bearer / Basic、JWT、`sk-`、`gh*_`/`github_pat_`、`xox[baprs]-`、`AKIA`、`AIza`、`hf_`、`npm_`、`ya29.`、赋值型 secret（`password=`、`token:` 等）、URL 内嵌凭据、URL 查询参数 token、邮箱。映射是幂等的：对已脱敏文本再跑一次结果不变，因此日志可以安全重复处理。

`redact.Findings` 只返回规则名（如 `PRIVATE_KEY`、`EMAIL`），从不返回匹配到的原文，可以安全写入日志或返回给前端。`Text` 是保守规则而非完备检测，预览始终需要人工确认。

## 幂等、回执与未知结果

- Outbox 主键 `intent` 唯一。派发前在单事务内插入 `INFLIGHT` 行；插入成功者就是唯一派发者。并发重复请求读到同一行，返回同一记录并标记 `replayed:true`，Mock 侧计数证明只提交一次。
- 同一 `intent` 不同内容（payload hash 不同）返回 `422 IDEMPOTENCY_CONFLICT`，不覆盖旧记录。
- 有效回执必须同时匹配 `taskId`、`sessionId`、`digest`、`checkpoint`，且 `receiptId` 非空。`state` 先做去空格与大写规范化，然后必须**严格等于** `DELIVERED`；缺失 `state` 与其它取值一样视为 `RECEIPT_MISMATCH`，不能因为没有写状态就当作成功。空对象、字段不符同理。
- `acceptedAt` 只在适配器给出非零时间时保存为 RFC3339；零值保存为空字符串，界面显示"接受时间未知"，不会伪造 `0001-01-01`。
- 结果分类：

| 情形 | 记录状态 | HTTP | 是否自动重投 |
|---|---|---|---|
| 正常回执 | `DELIVERED` | 200 | — |
| `TIMEOUT` / 适配器 panic / 空回执 / 回执不匹配 / 其他适配器错误 | `UNKNOWN` | 200（记录可见） | 否 |
| `OCCUPIED`、`CHANGED`、`UNAVAILABLE`、会话不存在或 owner 不符 | `REJECTED` | 409/503/404 | 否 |
| 同 intent 已存在 | 原状态 | 200 | 否 |

  `UNKNOWN` 返回 200 而不是错误码，因为任务确实可能已经送达；界面必须展示"未知"而不是失败。错误码 `UNKNOWN_RESULT`、`RECEIPT_EMPTY`、`RECEIPT_MISMATCH`、`RECEIPT_CONFLICT`、`ADAPTER_FAILURE` 只在内部记录与监控中使用。
- 超时后再次提交同一 intent 只会得到 `UNKNOWN` 记录，不会重新派发。`POST /tasks/{id}/reconcile` 只调用 `Adapter.Reconcile`；Mock 的提交计数不增加，核对后若找到回执才转 `DELIVERED`。
- 应用重启时，所有遗留 `INFLIGHT` 行在同一次迁移后的启动事务中改为 `UNKNOWN`，并标注"绝不自动重投"。

## 检查点与防分叉

- 检查点只在 `DELIVERED` 后写入 `checkpointHash` 并置 `checkpointAdvanced:true`。
- **主链只接受最新已交付父**：cp1、cp2 都交付后，再拿 cp1 的 `parentHash` 提交 cp3 会被拒绝为 `CHECKPOINT_BRANCH`。父必须等于该链最新 `DELIVERED` 检查点。
- **相同父只能有一个未拒绝后继**：`task_chain_slots` 以 `(chain_task_id, session_id, kind, slot)` 为主键，`kind=PARENT` 一行即代表该父已被某个 `INFLIGHT`/`DELIVERED`/`UNKNOWN` 后继占用。两个不同 checkpoint 并发抢同一父时只有一个事务能插入成功。
- 占用不只是内存锁：`claim` 在同一个 SQLite 事务里插入 Outbox 行与两个 slot（`CHECKPOINT` 与 `PARENT`），主键冲突即回滚。因此多进程或进程重启后规则不变。
- **`UNKNOWN` 占用检查点**：超时、空回执、回执不匹配都会保留 slot。换一个 intent、改内容再提交同一 `task/session/checkpoint` 仍返回 `CHECKPOINT_IN_USE`，只有有效 `reconcile` 把记录推进到 `DELIVERED` 后，后继才能继续。`REJECTED` 不占用，因为请求根本没被宿主接受，允许用新 intent 诚实重试。
- 父检查点未交付返回 `PARENT_NOT_DELIVERED`；整条链完全不存在返回 `PARENT_CHECKPOINT_UNKNOWN`；跨链或旧父返回 `CHECKPOINT_BRANCH`；哈希不符返回 `PARENT_HASH_MISMATCH`。
- 同一检查点只能有一条活动记录，重复使用返回 `CHECKPOINT_IN_USE`；已交付首检查点后再提交 `parentCheckpoint=0` 的同链新包返回 `CHECKPOINT_BRANCH`。
- 旧版本写入、没有 `chain_task_id` 的行在启动时一次性补写并重建 slot；该行仍为 `INFLIGHT` 时会被转为 `UNKNOWN` 并继续占用检查点。

## 会话所有权

Mock 只有三条合成会话，`owner` 固定为 `official-host`，`kind` 为 `SYNTHETIC_REFERENCE`。桥接**只引用**：会话不存在直接 404，owner 不符直接 409 `SESSION_OWNER_MISMATCH`，不创建、不接管、不改写会话。

## 模拟适配器模式与行为

模式：

| 模式 | Capabilities | 效果 |
|---|---|---|
| `AVAILABLE` | 全开 | 正常 |
| `CHANGED` | 仅 `sessions`、`reconcile` | 提交返回 `ErrChanged`，`degraded` 列出被禁能力 |
| `UNAVAILABLE` | 全关 | 会话读取与提交都不可用 |

降级是局部的：`CHANGED` 和 `UNAVAILABLE` 期间 Outbox 历史、插件、用量与模型目录照常工作；`UNAVAILABLE` 时桥接返回 409 `ADAPTER_DEGRADED`，不写入 Outbox。

行为：`OK` 返回确定回执；`TIMEOUT` **先登记回执再返回超时错误**，用于验证"可能已送达"的真实语义；`EMPTY` 返回空回执错误；`OCCUPIED` 返回占用错误；`PANIC` 主动 panic，由桥接的 `recover` 隔离。Mock 不启动官方进程，不读取本机配置、Cookie、会话或凭据。

## 插件

插件不在本文件描述的桥接层里，而是核心的本地插件宿主：包结构、清单、能力、运行时接口、生命周期与 HTTP 接口见 [PLUGINS.md](PLUGINS.md)。这里只记录与桥接相关的约束。

- **没有内置模拟插件**：不存在 `kct`、`monitor`、`fault-demo` 这类进程内模拟定义，也没有 `registerBuiltins`、`FAULT` 动作或"模拟资源计数"。插件列表只反映真实安装。
- **插件不是桥接适配器**：插件通过 `/enhance/v1/plugin-runtime/{action}` 取用核心能力，与 Outbox、任务投递、原生适配器互不影响。桥接降级时插件照常工作。
- **进程内运行**：插件是运行在宿主渲染进程里的 ES module，核心不启动 Node 服务，也没有插件宿主子进程。
- **安全模式**：`safe func() bool` 为真时拒绝安装与启用，并停止已运行插件、撤销其身份令牌；关闭后可以重新启用。插件页同时显示该状态。
- **`Close` 是终止操作**：撤销全部令牌，之后安装、启用、动作调用都返回 `MANAGER_CLOSED`。
- **不修改官方包体**：插件只读取本机数据目录，不写入 Kimi Code 官方存储，不回写官方凭据。

Monitor 与 KCT 是后续阶段的适配插件，本阶段不实现；它们将来只能经上述接口接入，核心不为其保留私有通道或硬编码挂载。

## 与 `docs/API_CONTRACT.md` 的精确偏差

1. **`POST /tasks` 增加 `confirmationHash`、`acknowledgeUnverifiedAttachments` 两个可选请求字段**，用于绑定预览和显式接受附件缺口。契约原文只列了 `intent`、`task`、`confirmed`、`mockBehavior`。未知字段会被 `httpx.Decode` 拒绝，所以这两个字段是新增的显式契约项。
2. **`POST /tasks` 的 `mockBehavior` 只影响 Mock 行为的注入**，不改变校验规则；生产适配器可忽略该字段。行为枚举大小写不敏感，规范化为大写。
3. **`UNKNOWN` 以 200 + 记录形式返回**，因为任务可能已经送达，界面必须展示"未知"而不是失败。**`REJECTED` 返回对应的 HTTP 错误码**（`409`/`503`/`404`，见上文结果分类表），不会以 200 伪装成功；记录本身仍保留在 Outbox 中，可通过 `GET /tasks` 查看。
4. **新增 `GET /enhance/v1/plugins/health`**。契约只列 `GET /plugins`；健康聚合是额外只读端点。
5. **`GET /plugins` 响应增加 `notice` 与每个插件的 `sandbox`、`controlled` 字段**；`items` 内包含 `enabled`、`faults`、`error`、`resources`、`safeMode`。
6. **`GET /sessions` 响应增加 `mock`、`ownership`、`notice`**，会话项增加 `kind`。
7. **`GET /tasks` 记录额外包含 `findings`、`confirmationHash`、`checkpointHash`、`checkpointAdvanced`、`note`、`replayed`、`createdAt`、`updatedAt`**。
8. **`TaskPackage` 的 `messages[]` 额外接受并返回 `sequence`**；`trust` 由服务端强制覆写为 `untrusted-web`。
9. **`linkedTaskId` 的语义收紧**：带 `parentCheckpoint>0` 时必填，且必须指向父检查点所属任务。契约只把它列为可选字段。
10. **附件上限为 1 MiB/项**，通过独立上传写入私有 store；任务 JSON 只保留引用。store 总量和保留策略见 API_CONTRACT.md。
11. **`GET /tasks` 的 `total` 由 SQL `COUNT(1)` 得出**，`items` 只包含当前 `LIMIT/OFFSET` 页；契约未规定分页实现。
12. **记录 `id` 由 `intent + payload hash` 派生**（`task-` + 16 位十六进制）。早期版本只由 payload 派生，导致 `REJECTED` 后合法重试撞主键；该行为已修正。
13. **`POST /plugins/{id}` 新增错误码** `PLUGIN_ENABLING`、`PLUGIN_ACTIVATION_CANCELLED`、`MANAGER_CLOSED`；契约只规定了三个 action。
14. **`receipt.state` 必须显式为 `DELIVERED`**；契约示例省略了该字段，本实现把它当作回执有效性的一部分。

## 验证

工具链：Go 1.27.3，依赖 `modernc.org/sqlite` 纯 Go 驱动。以下命令在项目根目录、Go 已位于 PATH 时执行。

```bash
go test ./internal/redact ./internal/desktop ./internal/bridge ./internal/plugins
go test -race ./internal/redact ./internal/desktop ./internal/bridge ./internal/plugins
go vet ./internal/redact ./internal/desktop ./internal/bridge ./internal/plugins
go test ./internal/bridge -run TestModulesShareOneMuxAndStorage -v
```

下列计数为 0.1.0 历史记录；0.1.1 的实际全工程结果和原始日志以 `VALIDATION.md`、返回包 evidence 为准。

实际结果：78 项测试全部通过（bridge 36、plugins 27、desktop 9、redact 6），`-race` 无报告；`vet` 无输出；`bridge` 与 `plugins` 以 `-count=3 -race` 重复运行稳定通过。`bridge` 包内集成测试在同一 `http.ServeMux` 上同时挂载三个模块，并在同一个 `*sql.DB`（`SetMaxOpenConns(1)`）上完成预览、提交、回执、插件启用与健康聚合，未出现 `Rows` 与 `Exec` 嵌套死锁。

覆盖到的具名场景：多行私钥与其它 secret 遮蔽（预览、持久记录、Mock payload 三处均检查）、空回执不成功、`UNKNOWN` 不自动重投、超时后只核对不提交、并发唯一派发、同键不同 body 冲突、事务回滚、会话所有权拒绝、重启 `INFLIGHT`→`UNKNOWN`、插件并发启停与故障隔离。

本轮新增的边界回归（`internal/bridge/chain_test.go`、`internal/plugins/lifecycle_test.go`）：

| 场景 | 断言 |
|---|---|
| cp1/cp2 交付后再用 cp1 作父 | `CHECKPOINT_BRANCH`，且不落库 |
| 8 个不同 checkpoint 并发抢同一父 | 仅 1 条 `DELIVERED`，slot 表中该父只有 1 行 |
| 超时后改内容 + 换 intent | `CHECKPOINT_IN_USE`，Mock 提交计数仍为 1 |
| `REJECTED` 后用新 intent 重试 | 成功交付（拒绝不占用检查点） |
| 适配器回执缺 `state` | 保持 `UNKNOWN`，reconcile 也拒绝 |
| 适配器给出零 `acceptedAt` | 保存为空串，不出现 `0001-01-01` |
| 25 条记录分页 | SQL `total` 正确，第 2 页与末页均正确 |
| 旧库 `chain_task_id` 为空 | 启动时补写、重建 slot、仍占用检查点 |
| 激活期间并发 `ENABLE` | 激活函数只执行 1 次，资源 2 项 |
| 激活期间 `DISABLE` / 安全模式 / `Close` | 返回取消错误，不复活、不残留资源 |
| 抛 panic 的 disposer | 其余 disposer 全部执行 |
| `EnforceSafeMode` 关闭后 | 可重新 `Enable`；`Close` 后返回 `MANAGER_CLOSED` |
| 首次激活被 `DISABLE` 取消后第二次成功，旧激活迟到失败 | 第二次保持 `ENABLED`、资源 2 项、`faults` 不变；旧调用返回 `PLUGIN_ACTIVATION_CANCELLED` |
| 安全模式取消后恢复并成功，旧激活迟到失败 | 同上，安全模式可逆不被旧失败破坏 |
| `Close` 期间激活迟到失败 | 资源全部释放，状态不发布 |

SQLite 事务回滚由 `storage.Transaction` 保证；`claim` 在单事务内写 Outbox 行与两个 slot，其中任何一步失败都会整体回滚，不会留下半条记录。所有诊断查询都在事务显式回滚之后执行，避免 `SetMaxOpenConns(1)` 下的连接自我等待。

### 未验证 / 限制

- 本文件所述旧 Outbox 没有对接真实官方宿主或真实网站上传，生产任务目标仍为 Mock；幂等只在该 Outbox 语义内成立。0.1.1 的本地 HTTP 接收端验证仅证明附件原字节传输。当前原生 KAP `handoff` 已能向真实 Code 会话发送文本，`session.send` 可发送本机文件路径，但属于另一条链路，不使用旧 Outbox 的附件上传或幂等回执。系统文件选择窗口的 GUI 点选、网站上传仍未验收。
- 没有验证真实网站 DOM、草稿写入或回复完成；`source.url` 仅保存引用。
- 没有操作系统级沙箱、独立进程、签名或卸载验证；插件隔离仅限 Go 层 `recover` 与资源作用域。
- 脱敏是规则匹配，不能保证识别全部敏感内容；仍需人工核对预览。
- 服务层通过 `Manager.EnforceSafeMode` 响应安全模式开关，该调用可逆、可重复；`Manager.Close` 只在进程退出时调用，调用后 `Enable` 返回 `MANAGER_CLOSED`。两条路径互不复用，安全模式不会终止插件管理器。
