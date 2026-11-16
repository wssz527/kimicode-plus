# 模型中心与用量中心

本文档补充 `docs/API_CONTRACT.md` 中模型与用量两节，说明已实现语义、契约偏差、错误码、存储边界与验证方法。两个模块均为独立 Go 包，只依赖 `database/sql`、`internal/storage` 与 `internal/httpx`，路由挂完整 `/enhance/v1` 路径，认证与 Host/Origin 校验由核心中间件负责。

原生宿主入口与当前额度边界见 `NATIVE_INTEGRATION.md`。Monitor 可直接读取隔离官方 `/oauth/usage` 并区分 AVAILABLE/ERROR/UNKNOWN，只有 AVAILABLE 且无连接错误时才绘制额度已用比例，错误时不沿用旧比例。侧栏默认紧凑，缺少输入/输出/速度/缓存命中样本时显示 `--`，诊断保留 UNKNOWN。自动化回执放在详情中，不是缓存命中率，也不写入本文四账本；原生意图和回执见 `internal/desktop/`。当前检查不证明真实账号额度或真实速度/缓存观测已验收。

## 模块与文件

| 路径 | 职责 |
|---|---|
| `internal/registry/` | 供应商预设与自定义地址、模型身份、逐字段来源合并、批量预览、事务导入、CAS、变更快照、回滚、Mock 目录发现、models.dev 导入、只读 Mock 网关 |
| `internal/usage/` | 协议归一化、原始记录与 provenance、canonical 去重、请求与 attempt、四账本、历史过滤分页、Mock demo、额度与余额快照 |
| `docs/MODELS_USAGE.md` | 本文件 |

入口为 `registry.New(db *sql.DB) (*Service, error)` 与 `usage.New(db *sql.DB) (*Service, error)`，随后 `Register(mux *http.ServeMux)`。迁移通过 `storage.Migrate(db, module, []string{...})`，`schema_versions` 分别记录 `registry` 与 `usage` 两个模块版本。

## 契约偏差与补充

以下是相对 `docs/API_CONTRACT.md` 的差异，均已在代码中固定；前端与整体验收以本表为准。

1. **能力字段集合扩大**。契约示例写 `{context,output,vision,tools,thinking,...}`。实际允许字段为 `context,input,output,cacheTTL,vision,video,audio,tools,thinking`。`context/input/output/cacheTTL` 只接受正整数或 `null`，其余只接受布尔值或 `null`；未知字段返回 `UNKNOWN_FIELD`。`cacheTTL` 只有明确来源才写入，不是默认 25 分钟。
2. **来源枚举收敛为五值**。设计文档还提到 `litellm`，契约只列 `manual|official|registry|modelsdev|UNKNOWN`，实现只接受这五值，`litellm` 会返回 `INVALID_SOURCE`。
3. **`fetchedAt` 必填（UNKNOWN+null 除外）**。契约把它标为 `ISO8601|null`；实现要求任何带来源或值的字段都有 `fetchedAt`，否则 `MISSING_PROVENANCE`。缺少来源不是“默认 modelsdev”，而是拒绝写入。设计文档中的 `updatedAt` 作为输入别名接受，输出统一为 `fetchedAt`。
4. **时间按真实时刻比较**，不按字符串。同一来源内比较 `fetchedAt` 解析后的时刻，因此 `2026-11-10T10:00:00+09:00`（01:00Z）早于 `2026-11-10T09:00:00Z`。写入数据库时统一规范化为 UTC RFC3339Nano。
5. **`/models` 分页**。契约未列出 `offset/limit`，实现按全局约定支持 `?offset=0&limit=100`（上限 1000），返回 `{items,total,offset,limit}`。
6. **`/models/preview` 与 `/models/import` 的 `records` 结构**。使用 Model 输入形态 `{providerId,id,name?,displayName?,capabilities:{field:{value,source,fetchedAt,sourceUrl?}}}`。`id` 是模型 ID，`name` 为显示名。预览不写库，只返回 `{revision,changes,summary}`；导入要求传入当前 `revision`。
7. **导入自动补齐供应商**。为了不让“导入目录”依赖另一次写操作，`import`/`modelsdev` 会为记录中的新 `providerId` 插入一条占位供应商（命中内置预设时使用预设名称/协议/地址，否则 `protocol=openai`、`baseUrl=urn:unconfigured`）。这类占位不发起任何请求，用户可通过 `PUT /providers/{id}` 配置其地址，保留模型关联。`POST /providers` 仍用于新建供应商，重复创建同 ID 返回 `PROVIDER_EXISTS`。
8. **`/models/modelsdev` 请求体**。`catalog` 是 models.dev `providers` 映射（也接受完整导出对象，只要含 `providers`），必须包含 `retrieved_at`，作为全部字段的 `fetchedAt`；`source` 作为字段 `sourceUrl`。响应为 `{preview,revision,changes?,summary,import?}`；`preview:false` 时 `import` 是标准导入回执。
9. **`/models/override` 的 `value` 必须出现**，`null` 表示手动 UNKNOWN。缺少该键返回 `MISSING_VALUE`，避免把“未提供”误当“清空”。响应额外返回 `model/before/after/field`。
10. **`/model-changes/{id}/rollback` 幂等性**。同一变更第二次回滚返回 `ALREADY_ROLLED_BACK`（409），不重复写入。回滚本身也生成一条 `kind=rollback` 的变更快照，可继续审计。
11. **`/gateways` 只读且余额 UNKNOWN**。返回 `items:[{id,name,kind,balance:null,currency:null,checkedAt:null,note,readOnly:true}]`，不访问远端，不读取密钥。
12. **用量事件新增可选字段**：`requestKind`（`chat|coding|compaction|review|gateway|UNKNOWN`）、`counts`（直接指定归一化计数）、`observationId`（预留，当前由内容生成）。`kind` 只接受 `request`（默认）与 `snapshot`；`snapshot` 只计数为 `snapshots`，永不计费。
13. **`/usage` 响应附带 provenance**：`{items,total,offset,limit,provenance:{eventId:[{observationId,source,authority,role,at,recordedAt}]}}`，避免前端为每条事件再发一次请求。
14. **`/usage/summary` 字段扩展**。除契约的 `requests/input/output/cacheRead/cacheWrite/estimate/knownRequests` 外，新增 `fresh`、`partialKnown`（每字段 `{sum,known,total,complete}`）、`coverage`（0–1 比例）、`partialKnownRequests`、`unknownRequests`、`actual`、`subscription`、`filters`、`generatedAt`、`note`。**顶层合计只有在全部事件都有该字段时才给数字**，否则为 `null`，部分和放在 `partialKnown`。空结果返回 `null` 而不是 `0`。
15. **`estimate` 为对象**：`{kind:"ESTIMATE"|"UNKNOWN",amount,currency,currencies,complete,pricedEvents,unpricedEvents,unknownUsageEvents,partialKnown,priceSources,priceFetchedAt,unit:"per_million_tokens",note}`。多币种时不合并成单一 `amount`。没有价格或计数未知时为 `UNKNOWN`，不显示为免费。
16. **`actual` 与 `subscription` 独立**：`actual` 来自显式账单观察（`POST /usage/actual`），`subscription` 来自 `SUBSCRIPTION` 额度快照，两者都不由 Token 推算，也不互相换算。账单行只有 provider 与时间维度，因此 `actual.attribution` 恒为 `PROVIDER_LEVEL`：只有筛选只含 `providerId`（可选 `from`/`to`）时，`actual.kind` 才是 `PROVIDER_LEDGER` 并给出 `amount`；一旦带 `projectId`/`sessionId`/`agentId`，`amount` 保持 `null`，金额只出现在 `actual.providerLedger` 并注明无法归属。仅显式 `from`/`to` 会收窄账单时间窗，不会用事件时间戳反推。
17. **`POST /usage` 的响应**扩展为 `{added,duplicates,updated,snapshots,unresolved,total}`。`duplicates` 覆盖“同 attempt 重复上报”，`updated` 覆盖“更高权威来源替换同一行”。
18. **`PUT /enhance/v1/providers/{id}`（新增）**：输入同 create（`id` 可省略但若提供必须与路径一致，否则 `PROVIDER_ID_IMMUTABLE`），并必须带 `revision`，缺省返回 `MISSING_REVISION`。响应 `{revision,changeId,provider,snapshot:{providerId,before,after,fields:[{field,before,after}]}}`。该写入与模型导入共用同一个 registry revision，属于 CAS：revision 过期返回 `STALE_REVISION`。它同时用于把目录导入自动产生的 `urn:unconfigured` 占位供应商改成真实地址，模型关联按 `providerId` 保留。变更写入 `registry_changes(kind=provider)` 与 `registry_provider_changes`，`GET /model-changes/{id}` 的 `providers` 字段可查看前后配置，`POST /model-changes/{id}/rollback` 会一并恢复供应商配置（`restoredProviders` 计数）。不发起任何网络请求。
19. **新增只读接口**（契约未列，但为四账本与未知核对所需）：
    - `GET /enhance/v1/usage/unlinked`：无法可靠关联的原始观察，`{items,total,offset,limit}`，永不并入合计。
    - `GET /enhance/v1/usage/provenance`：分页返回每条事件的来源链接。
    - `POST /enhance/v1/quotas`：写入额度快照 `{ledger,providerId,scope?,remaining?,limit?,unit?,resetAt?,currency?,source,observedAt?,note?}`，`ledger` 为 `SUBSCRIPTION|GATEWAY|PROVIDER`，`source` 为 `official|gateway|manual|mock`。
    - `POST /enhance/v1/usage/actual`：写入实际计费观察 `{observationId,providerId,amount,currency,source,observedAt?,note?}`，`observationId` 用于去重。
    - `GET /enhance/v1/model-changes/{id}`：单条变更详情，含变更前后的完整模型快照与 `providers` 供应商快照。
20. **`GET /quotas` 额外返回四账本目录** `ledgers:[{ledger,label,remaining:null,known:false,note}]`，明确当前未知的部分，方便前端不做补零。
21. **`POST /usage/demo`** 确定性地写入 8 条固定事件（含一次真实重试、一个未知计数、一个 snapshot、一个无法关联的 wire 记录）与两条 Mock 额度快照；重复调用只增加 `duplicates`，不新增事件、不累加金额。

## 模型合并语义

字段主键是 `(providerId, modelId)`，不同供应商的同名 ID 不合并。每个字段独立保存 `value/source/fetchedAt/sourceUrl/sourceRevision`。

优先级 `manual(5) > official(4) > registry(3) > modelsdev(2) > UNKNOWN(0)`。规则：

- 优先级不同时高者胜；`UNKNOWN + null` 的输入永远是“无观察”，不参与覆盖。
- **来源与时间在写入前规范化并持久化**：`source` 会被裁剪空白（`" manual "` → `manual`），`fetchedAt` 统一转为 UTC RFC3339Nano（`2026-11-10T10:00:00+09:00` → `2026-11-10T01:00:00Z`）。这条规则同时作用于 preview、import、override 和已存快照的读取，避免带空白的来源在优先级查表中落到 0 而被低优先级目录覆盖。合法的 `UNKNOWN + null` 原样保留。
- 同来源比较解析后的时刻，较新者胜；同刻不覆盖，保持先到者，结果与批次内顺序无关。
- 手动 `null` 是有效覆盖：`source=manual,value=null,fetchedAt=...`，后续任何目录刷新都不能把它填成猜测值。
- 缺失字段保持 `source=UNKNOWN,value=null,fetchedAt=null`；显式 `false` 与 `null` 严格区分。
- 显示名保存自动名称与可选人工显示名，只有 ID 的刷新不清空名称。自动能力候选单独持久化。manual UNKNOWN 仍阻挡自动值；`POST /models/metadata` 的 `restoreAutomatic:true` 才取消覆盖。旧版本已丢失的候选无法凭空恢复。

## 事务、CAS 与回滚

- `GET /registry/snapshot` 在单事务中读取当前 revision 和编辑实体。草稿固定该基线，列表轮询不能续签。
- `registry_revisions` 只增；有效 no-op 不推进版本、不写空变更。历史缺口由 snapshot 的当前版本处理，无需删库。
- import、override、metadata、rollback 在单事务内校验基线、计算 diff、写快照及实体。任一步失败整体回滚。
- 回滚先预览实际冲突；提交时比较 live entity 与原 after-state。后续实体修改返回 `ROLLBACK_CONFLICT`，不覆盖新值。版本过期返回 `STALE_REVISION`。成功恢复/删除相应模型并记录新快照及 rollback 身份。
- provider 创建和更新与模型共用 revision。创建原子保存 `provider-create` 历史及 `before:null` 的快照；重复 ID 不推进版本。更新字段未变时不推进版本、不写历史。创建回滚只删除未被模型引用且仍匹配 after-state 的供应商，已有模型则返回 `ROLLBACK_CONFLICT`。更新快照保留完整 before/after，回滚目录不会误改供应商配置。
- 模型列表在一个只读事务读取计数、页键与字段。数据库最大连接数为 1，事务内复用同一个 `*sql.Tx`。

## 用量去重与归一化

canonical 键：`providerId + requestId + attemptId + sessionId + agentId`。规则：

1. 一次 attempt 保留一个累计行，每次观测记录 lifecycle/sequence/counts/decision。相同来源的有序 PARTIAL → FINAL 可以补全，重复终态不累加，旧序号不能覆写终态。终态数值修订需显式 CORRECTION。
2. 冲突来源保留观测及 conflict 状态；更高 authority 的 partial 不能擦掉已知值，不逐字段取最大。
3. 真正重试的 attemptId 不同，分开计数。缺 attempt/session/agent 进入 usage_unlinked，不参与合计，不按时间或相似 Token 猜关联。

`authority`：`manual(4) > official(3) > gateway/broker(2) > monitor/mock(1) > UNKNOWN(0)`。

归一化（`usage.Normalize`）：

| 协议 | 输入 | 缓存读 | 缓存写 | 输出 |
|---|---|---|---|---|
| `openai` / `responses` | `prompt_tokens` 或 `input_tokens`（含缓存读子集） | `*_tokens_details.cached_tokens`，`fresh = input - read` | 恒为 null | `completion_tokens` 或 `output_tokens` |
| `anthropic` | `input_tokens` 只是 fresh；总量 = `input + cache_read + cache_creation` | `cache_read_input_tokens` | `cache_creation_input_tokens`（或 `cache_creation` 两档之和） | `output_tokens` |
| `gemini` | `promptTokenCount`，`fresh = input - cached` | `cachedContentTokenCount` | 恒为 null | `candidatesTokenCount + thoughtsTokenCount`，未报 thoughts 时为 null |
| 其它 | 全部 null | | | |

计数只接受非负整数，字符串、负数、非整数一律视为未知；`0` 是已知值。任一加数为 null 时合计为 null，绝不以 0 补位。

## 四账本

| 账本 | 来源 | 接口 | 缺失时 |
|---|---|---|---|
| 实际上报 Token | 已完成请求、官方记录、受支持响应 usage | `GET /usage`、`GET /usage/summary` 的 `input/output/fresh/cacheRead/cacheWrite` 与 `coverage`、`partialKnown` | `null`，覆盖率为比例而非 0 |
| 估算费用 | Token × 带时效单价（`/million tokens`、币种、source、fetchedAt） | `summary.estimate` | `kind=UNKNOWN`，amount=null |
| 实际计费/供应商余额 | 显式账单观察 | `summary.actual`、`POST /usage/actual` | `kind=UNKNOWN`，不拿估算冒充 |
| 官方订阅额度/网关余额 | 显式额度快照 | `summary.subscription`、`GET /quotas`、`POST /quotas` | `remaining=null` |

价格保存在 `usage_prices`，以 `(providerId,model)` 唯一，按事件携带的 `price` 写入且只在 `fetchedAt` 更新时覆盖；事件同时保存价格快照。快照（`kind=snapshot`）不写费用、不进入账单。

### 按协议的计费口径

费用只按协议真正上报并计费的字段计算，`null` 永远不会被当作 0，也不会用总量单价顶替缺失的缓存拆分：

| 协议 | 必需字段 | 可选/协议 N/A |
|---|---|---|
| `openai` / `responses` | `fresh`、`output`、`read`（含 `cached_tokens` 未知的情况） | `write` 为 N/A，不影响计费 |
| `anthropic` | `fresh`、`output`、`read`、`write`（缺任一即 UNKNOWN） | 无 |
| `gemini` | `fresh`、`output`（含 thoughts）、`read` | `write` 为 N/A |

- 缺任一必需计数 ⇒ 该事件不计费，计入 `estimate.unknownUsageEvents`。典型：Anthropic 只有 `input=100/output=20`、缺 cache_read/cache_creation；OpenAI 只有 `input=100/output=20`、`cached_tokens` 未知。后者不能用 input 单价把可能属于折扣缓存的输入按原价计。
- `read > input`、`write > input` 或 `fresh + read > input` 属于不一致数据，拒绝计费并计入 `estimate.inconsistentUsageEvents`，不会双计。
- OpenAI/Gemini 出现 `cacheWrite > 0` 属于协议 N/A 字段的矛盾计数，按 `UNKNOWN/INCONSISTENT` 处理，不读取不存在的单价。
- 聚合结果再次校验有限性；浮点溢出或负值计为 `INCONSISTENT`，不输出 Inf/NaN。
- 未知协议（不在上表内）计入 `estimate.unknownProtocolEvents`。
- 价格本身缺 `currency` 时计入 `unpricedEvents`；`cacheRead`/`cacheWrite` 单价只在对应计数 `>0` 时才必需。已明确 `cached_read=0` 时不需要缓存读单价，普通 input/output 仍可精确估价（已知 0 不产生缓存费用，未知才不可补零）。
- OpenAI/Gemini 的 `cacheWrite` 是协议 N/A：即使 `Counts.Write>0` 与协议矛盾，也只返回 `UNKNOWN/INCONSISTENT`，不会读取不存在的单价或崩溃。

### 价格 JSON 边界

`price` 的 `input` 与 `output` **必须显式出现且非 null**，否则请求返回 `400 INVALID_JSON`，不会把缺省解码出的 `0` 当成免费价格。显式 `"input": 0` 是合法的零费率。`cacheRead`/`cacheWrite` 可省略（表示该档未知/不适用），`price` 内任何未知字段都会被拒绝。`input`/`output`/`cacheRead`/`cacheWrite` 必须是有限数值；`1e400` 这类会溢出为 Inf 的输入在解码时即被拒绝，聚合端也不会输出 Inf/NaN（溢出事件计入 `inconsistentUsageEvents`）。

### 实际账单写入边界

- `amount` **必须显式出现且非 null**；缺失或 null 表示未知，返回 `400 INVALID_JSON`，不会被当成 0 记账。显式 `0` 是合法的零金额。
- `amount` 必须是有限非负数，且**小于 `1e12`**（1 万亿，按百万分之一单位存储）。上限取 1e12 而非 int64 的精确边缘，是因为 float64 在接近 2^63 时无法区分相邻 micro，跨平台判定会不一致；1e12 在所有平台退化为相同的整数比较。边界附近的十进制值本身可能已被 float64 舍入，接口不承诺“刚好差一个 micro”的精度。超出返回 `400 INVALID_AMOUNT`。
- `price`/`amount` 中的 `input`、`output` 等数值必须是有限值；`1e400` 这类溢出为 Inf 的输入在解码阶段即被拒绝，聚合端也不会输出 Inf/NaN。
- 同一 `observationId` 重复提交：
  - 规范内容完全相同（provider、micros、currency、source、observedAt、note）→ `200`，回读并返回**已持久化**的那条记录，附带 `"duplicate": true`，不重复记账。
  - **省略 `observedAt` 的重试**：首次写入生成时间；后续同 `observationId`、同其余内容的请求复用已保存时间并返回 `duplicate:true`，不会因为“现在是另一个时刻”而冲突。并发重复同样以已持久化行为准。
  - 显式给出**不同的** `observedAt`，或任一其他字段不同 → `409 OBSERVATION_CONFLICT`，拒绝覆盖既有账单，需使用新的 `observationId`。
- 响应中的金额始终来自落库值，不会把未持久化的输入伪装成已保存。

### Mock demo 幂等

`POST /usage/demo` 的事件按 canonical 键去重；两条固定 Mock 额度快照（`SUBSCRIPTION/kimi/plan` 与 `GATEWAY/mock-gateway/balance`）按 `ledger+provider+scope+source+observedAt+note` 识别，已存在则跳过。重复调用时 `added=0`、`quotaSnapshotsAdded=0`，`usage_quota_snapshots` 行数不变，且不会改写用户创建的其他额度快照。

### 当前价回填的标注

如果事件本身没有价格、但同一 `(providerId,model)` 已有保存价格，聚合会使用该价格并在 `estimate.costBasis` 标明：

- `event_price`：全部使用事件携带的价格（历史快照）。
- `CURRENT_PRICE_FALLBACK`：部分事件使用保存价格，属于**事后按现价估算**。
- `CURRENT_PRICE_ONLY`：全部使用保存价格。

同时 `latePriceFallbackEvents` 给出使用回填价格的事件数，`complete` 置为 false，`note` 明确说明不是当时的历史价格。

## 错误码

| 状态 | code | 场景 |
|---|---|---|
| 400 | `INVALID_PROVIDER_ID` / `INVALID_MODEL_ID` / `INVALID_PROTOCOL` / `INVALID_NAME` | 身份或供应商字段不合法 |
| 400 | `INVALID_BASE_URL` / `INVALID_SOURCE_URL` / `INVALID_URL_QUERY` | 地址不是 http(s)、无法解析 |
| 400 | `CREDENTIALS_IN_URL` | URL 含 userinfo，或 query 键/值疑似凭据 |
| 400 | `SECRET_INLINE` | `secretRef` 看起来是内联密钥 |
| 400 | `MISSING_PROVENANCE` / `INVALID_SOURCE` / `INVALID_FETCHED_AT` | 字段来源或时间不完整 |
| 400 | `MISSING_VALUE` / `INVALID_VALUE` | override 的 `value` 缺失或类型错误 |
| 400/422 | `INVALID_CAPABILITY` / `UNKNOWN_FIELD` | 字段名或值不合法 |
| 404 | `PROVIDER_NOT_FOUND` / `NO_MOCK_CATALOG` / `MODEL_NOT_FOUND` / `CHANGE_NOT_FOUND` | 目标不存在 |
| 400 | `MISSING_REVISION` / `PROVIDER_ID_IMMUTABLE` | 供应商更新缺 revision 或试图改 ID |
| 409 | `PROVIDER_EXISTS` / `STALE_REVISION` / `ALREADY_ROLLED_BACK` | 冲突，需刷新后重试 |
| 400/422 | `EMPTY_EVENTS` / `TOO_MANY_EVENTS` / `INVALID_EVENT` / `INVALID_PRICE` / `EMPTY_RECORDS` / `EMPTY_CATALOG` | 用量、价格或目录输入不合法 |
| 400 | `INVALID_TIME_FILTER` / `INVALID_RANGE` | 历史过滤时间不是 RFC3339 或 from > to |
| 400 | `INVALID_LEDGER` / `INVALID_CURRENCY` / `INVALID_AMOUNT` / `INVALID_OBSERVED_AT` / `MISSING_OBSERVATION` | 账本写入不合法 |
| 409 | `OBSERVATION_CONFLICT` | 同一 `observationId` 已存在且规范内容不同 |

所有错误沿用 `{error:{code,message}}`。

## 安全边界

- 模型与用量模块自身不主动发起推理或余额请求。`/models/discovery` 只返回内置 Mock 目录，响应带 `mock:true,external:false`；`/gateways` 不查询远端。显式供应商目录发现由 `POST /providers/{id}/discover` 另行执行真实目录 GET；轻量聊天和本地网关的推理请求位于 `chat`、`provider`、`gateway`，不属于本模块。
- 只保存供应商地址与 `secretRef` 引用；拒绝 userinfo、疑似凭据的 query、内联密钥。
- 测试只使用临时目录 SQLite（`t.TempDir()`），不读取研究项目、真实配置、Cookie 或凭据。

## 接口示例（实际抓取）

以下为真实 loopback 服务与磁盘 SQLite 的实际请求/响应，仅摘自本地临时数据库。

### 目录导入后配置占位供应商

```
POST /enhance/v1/models/modelsdev {"catalog": <models.dev providers 映射>, "revision": 0}
HTTP 200
{"preview":false,"revision":1,"summary":{"created":8,"updated":0,"unchanged":0},
 "import":{"revision":1,"changeId":1,"imported":8,"created":8,"updated":0,"unchanged":0}}

PUT /enhance/v1/providers/anthropic
{"name":"Anthropic (configured)","protocol":"anthropic",
 "baseUrl":"https://api.anthropic.com","secretRef":"keychain://anthropic","revision":1}
HTTP 200
{"revision":2,"changeId":2,
 "provider":{"id":"anthropic","name":"Anthropic (configured)","protocol":"anthropic",
             "baseUrl":"https://api.anthropic.com","secretRef":"keychain://anthropic",
             "preset":true,"createdAt":"2026-11-10T13:28:58.50751Z"},
 "snapshot":{"providerId":"anthropic",
             "before":{"id":"anthropic","name":"Anthropic","protocol":"anthropic",
                       "baseUrl":"https://api.anthropic.com","secretRef":null,"preset":true,...},
             "after":{...},
             "fields":[{"field":"name","before":"Anthropic","after":"Anthropic (configured)"},
                       {"field":"secretRef","before":null,"after":"keychain://anthropic"}]}}

PUT /enhance/v1/providers/anthropic {... "revision":1}
HTTP 409
{"error":{"code":"STALE_REVISION","message":"revision 1 已过期，当前为 2"}}
```

`GET /models` 在更新前后 `total` 相同（示例为 8），模型的 `providerId` 不变。

### 账单归属

```
GET /enhance/v1/usage/summary?sessionId=s1
HTTP 200
{... "actual":{"kind":"UNKNOWN","amount":null,"currency":null,"currencies":["USD"],
  "observations":[{... "amount":12.5 ...}],
  "attribution":"PROVIDER_LEVEL","scope":{"providers":["anthropic"]},
  "providerLedger":{"amount":12.5,"currency":"USD",...},
  "note":"筛选包含项目/会话/Agent；账单缺少这些维度，因此不给该范围的实际费用，providerLedger 是供应商级总额"}, ...}

GET /enhance/v1/usage/summary?providerId=anthropic
HTTP 200
{... "actual":{"kind":"PROVIDER_LEDGER","amount":12.5,"currency":"USD", ...
  "note":"这是该供应商在时间范围内的账单总额（供应商级账本），不是按 Token 估算"}, ...}
```

### 不完整用量不得估算

```
GET /enhance/v1/usage/summary?sessionId=s1  （两条事件均缺缓存拆分）
"estimate":{"kind":"UNKNOWN","amount":null,...,
            "costBasis":"event_price","latePriceFallbackEvents":0,
            "inconsistentUsageEvents":0,"unknownProtocolEvents":0,
            "note":"没有可完整计费的事件，估算未知"}
```

## 验证

```bash
# 在项目根目录运行，Go 已在 PATH 中
export GOTOOLCHAIN=local
go vet ./internal/registry/ ./internal/usage/
go test -count=1 ./internal/registry/ ./internal/usage/
go test -race -count=1 ./internal/registry/ ./internal/usage/
```

`internal/registry/live_test.go` 会启动真实的 `127.0.0.1` HTTP 服务与磁盘 SQLite，依次执行 models.dev 导入、手动覆盖、变更查询、demo 写入、汇总，并重启后复查模型数与事件数。

测试覆盖的关键行为：事务中途失败完整回滚（模型、变更、revision 全部不变）、同来源时间按解析后比较、手动 null 阻断后续目录猜测、未知字段保持 UNKNOWN、并发去重、真实重试分开计数、snapshot 不计费、无法关联观察不并入合计、重启后查询、四账本分离且互不换算、Anthropic 缺缓存字段与 OpenAI 缺 cached_tokens 时估算为 UNKNOWN、完整用量按事件携带单价精确计价、`read>input` 拒绝计费、带 session 过滤时账单不给范围金额、当前价回填标注为事后估算、占位供应商可配置且模型关联不丢失、provider 更新的 CAS 冲突与回滚恢复。
