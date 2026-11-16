# 0.1.1 审查修复记录

> 历史范围：本文只记录 0.1.1 的审查、修复和当时证据。文中的“当前”“下一轮”及 BLOCKED 均指向该历史阶段，不描述 0.2.0 原生聚合或 2026-11-16 界面审查返修。当前统一规划入口、同屏会话、11 项内嵌设置、KAP 附件与未验收项见 [README](README.md)、[原生接入](docs/NATIVE_INTEGRATION.md) 和 [验证记录](docs/VALIDATION.md)。

基线源码归档 SHA-256：`b8dffd90a059d9a8ac7fea99021a711a18532674744fb0f9ad0718c43204dcd1`。现有 145 个源码文件与审查清单一致后，保存为提交 `98624444347ae60089461cc539acb4dc855269f1`。本轮在原工程修改，无技术栈重选或第二核心。日志路径以下均相对返回包 `evidence/`。

## F01 — 前后端供应商校验

首次反例：`config-red.log` 中旧 Go 接受远程 HTTP、query、fragment；`frontend-baseline-reproduced.log` 执行旧 API 与原表单正则，确认拒绝本地 HTTP 和点号 ID、接受后端拒绝的大写 ID。后者 PASS 表示缺陷成功复现，不是产品测试通过。

根因：表单宽泛正则与后端规则各自实现，URL 策略混入格式判断。

修复：`internal/registry/providers.go:178` 使用 URL 解析定义 HTTPS / loopback HTTP 策略；`internal/registry/model.go` 及 `web/src/lib/providerPolicy.ts` 统一 ID；`contracts/provider-policy.json` 提供 Go/TS 共享边界样本。短 IPv4 `127.1` 不因浏览器 canonicalize 被误判可用，query 中不允许存 Key。

提交：`a362b403254693a3ac6f03338bc65ad5b9888408`；边界补充 `6d0b367bb8163c0a8e1b4210141b619ad8bebc6b`。

回归：`TestReviewEndpointPolicy`、`TestReviewSharedProviderPolicy`，前端共享策略测试，包内 review acceptance 第 1 组。浏览器表单保存 `my.gateway` + `http://127.0.0.1:11434/v1` 并从 SQLite 读回。结果通过。

剩余范围：仅保存本地配置，不证明该网关或供应商推理可用。

## F02 — revision 来源与 no-op

首次反例：`config-red.log` 显示同内容导入把 revision 1 变成 2；旧前端 probe 只能从最新 change 推得 1，无法继续正确编辑。

根因：Import 提前 bump revision，前端从历史猜当前版本。

修复：`internal/registry/service.go:110` 有效 no-op 保持版本；`internal/registry/snapshot.go:21` 事务读取当前 revision 与实体；`web/src/api.ts` 使用明确 snapshot。已有库历史缺口不要求删库。所有写入 revision 缺省/null 拒绝。

提交：`a362b403254693a3ac6f03338bc65ad5b9888408`；HTTP 边界 `6d0b367bb8163c0a8e1b4210141b619ad8bebc6b`。

回归：`TestReviewNoopImportKeepsRevision`、`TestReviewSnapshotsGapAndTwoEditors`；包内重复导入、重新配对、继续编辑通过。UI 重复真实目录导入后保存供应商，SQLite 读回通过。

剩余范围：本地 revision 不代表官方配置具备跨进程 CAS。

## F03 — 固定编辑基线及回滚冲突

首次反例：旧前端 probe 把 revision 1 的草稿配上共享缓存 revision 2；`config-red.log` 中旧回滚删除了随后修改的模型。

根因：实体和版本非原子读取，保存时隐式使用共享最新 revision；回滚只校验全局版本，不比较实际 after-state。

修复：供应商/能力抽屉打开时取得原子 snapshot，固定 baseRevision 与实体副本，保存 API 必须显式传基线。`internal/registry/service.go:394` 在回滚事务中比较实体与原 after-state；`snapshot.go` 先返回回滚冲突预览。发生后续修改则整批拒绝，UI 不续签或丢弃旧输入。

提交：`a362b403254693a3ac6f03338bc65ad5b9888408`。

回归：两编辑者 Go/前端受控交错、`TestReviewRollbackRejectsLaterEdit`、包内 stale provider/capability/rollback 场景通过。真实 UI revision 2 草稿遇到另一编辑者 revision 3 后显示冲突且保留 `Preserved stale draft`，见截图与 `ui-concurrent-write.log`。

剩余范围：采用保守整实体拒绝，未提供强制覆盖或字段自动合并。

## F04 — 附件持久交付

首次反例：在隔离基线 archive 上运行兼容旧接口的 `TestReviewAttachmentPreviewPersistsBytes`，得到 `no such table: attachment_blobs`，证明 Preview 后无可恢复字节，见 `attachment-red.log`。

根因：校验后清空字节，任务只有描述，没有存储/授权/传输契约。

修复：`internal/bridge/attachments.go:58` 私有 SQLite blob、hash、容量、保留与回收；`:105` 限定任务引用读取器；独立有界二进制上传与鉴权下载。Outbox 同事务绑定 blob。UI 区分 VERIFIED / STORED / receipt UPLOADED，不展示原字节。预览不把文本脱敏当附件安全证明。

提交：`dcff2036c1972d20571ced71cce982412531acf5`。

回归：`TestReviewAttachmentRestartScopeAndHTTPReceiver` 在重启后把原字节送到本地 HTTP 接收端，逐字节/hash 匹配；未授权读取拒绝、引用保留、孤立回收通过。包内程序完成独立上传、Preview 后重启再派发、receipt 验证、再次重启下载及资源权限测试。零字节明确拒绝，1 MiB/件、64 MiB store。

剩余范围：真实 Kimi/网站上传仍未连接。UPLOADED 是已验证的 Mock 目标回执，不是官方平台送达。

## F05 — 派发许可及目标绑定

首次反例：`dispatch-red.log` 中初始检查后启用安全模式仍 submits=1，错误项目也 submits=1。

根因：安全模式只在早期检查，实际 transport 前没有与配置切换排序；会话项目/host 和 Web 目标契约不足。

修复：`internal/config/config.go:95` Dispatch 与 Put 共用许可互斥门，`internal/bridge/service.go:293` 真正调用位于门内；Session project/host 与实际 adapter 校验。Code 与 WebTransport 分开，Web 匹配 site/origin/tab/thread。

提交：`dcff2036c1972d20571ced71cce982412531acf5`。

回归：`TestReviewSafeModeWinsBeforeDispatch`、`TestReviewProjectBindingRequired`、`TestDispatchAndSafetySwitchAreOrdered`、`TestReviewWebTargetAndTransportIsolation`；包内错误项目/错误网页线程拒绝，MOCK_CODE 与 MOCK_WEB 回执明确区分，安全模式拒发通过。

剩余范围：已进入门的调用结束后安全模式写入才返回，不能宣称撤销已发送请求。真实适配器必须沿用此门并核实宿主接受/查询/附件回执，超时仍 UNKNOWN，不自动重投。

## F06 — 用量观测生命周期

首次反例：`usage-red.log` 中相同来源合法补全后 output 仍 nil。

根因：canonical 去重只按来源权威级比较，同来源后续 final 无法更新。

修复：`internal/usage/observations.go:20` 定义 PARTIAL/FINAL/CORRECTION 与 sequence，保留每次观测决定；`ledger.go` 更新同一 canonical attempt。终态去重、旧序号阻止、冲突保留、显式修订、不逐字段取 max；`normalize.go` 溢出保持 UNKNOWN。

提交：`127620dfb1e225968fdd149559de44e37c0a71c1`。演示清理补充 `6d0b367bb8163c0a8e1b4210141b619ad8bebc6b`。

回归：`TestReviewLaterUsageCompletionNotIgnored`、`TestObservationLifecycleOrderingConflictsAndRetry`、`TestNormalizeOverflowStaysUnknown`、`TestClearDemoPreservesOtherObservations`；包内 partial→final、重复、乱序、高可信 partial 冲突、retry 与清理保留非演示行通过。

剩余范围：仍未接真实官方/供应商 usage 事件，来源需后续映射实际生命周期。订阅额度、账单、估算与 UNKNOWN 保持独立。

## F07 — 名称及自动候选

首次反例：`config-red.log` 中仅含 ID 的刷新把已有名称变成 `m`。

根因：缺省名称回填模型 ID 后被当作新名称，缺少人工名称/上游名称和恢复自动候选语义。

修复：`internal/registry/model.go:64` 的持久模型记录分开 AutoName、ManualName、AutoCapabilities；`snapshot.go:66` 显式 metadata 编辑/恢复。ID-only 输入不清空名称，manual UNKNOWN 与 restoreAutomatic 分开。

提交：`a362b403254693a3ac6f03338bc65ad5b9888408`。

回归：`TestReviewIDOnlyRefreshPreservesName`、`TestReviewAutomaticRestoreAndNames`，包内别名保留及自动来源恢复通过。最终包内 UI 恢复 context 为 modelsdev 262144，SQLite 读回见 `ui-final-sqlite.log`。

剩余范围：0.1.0 已经丢弃的候选无法恢复，缺失保持 UNKNOWN；后续新目录可补自动候选。

## F08 — 原始日志及源码/产物绑定

首次反例：原审查 F08 的 archive/build-info 检查；本轮 `baseline-inventory.json` 再次确认旧 source 中引用的 work/evidence 原始日志没有随源码归档。

根因：只提供汇总结果，忽略的工作日志及 VCS/源码关系未纳入交付。

修复提交：`b05a085356d8c3d83681be31775758761652e7b1`，`scripts/evidence.py`、`scripts/review_acceptance.py` 记录命令、退出码、源码清单、commit/dirty 与被测 binary SHA；构建使用外部 manifest 明确绑定。最终文档/归档提交见 `REVIEW_STATUS.json`，不把自引用 SHA 写进同一提交。

验收：返回包包含 go race/vet、前端类型/测试/构建、真实 HTTP/包内验收与性能日志；每条结果有退出码，skip 独列。`source_manifest.json`、源码 ZIP、代码 patch、最终 clean commit、候选/发布包与测试二进制 SHA 均核验。包内程序逐字节匹配并通过 21 + 6 组验收。归档扫描拒绝 runtime.json、数据库、真实隐私路径；日志仅进行路径匿名化并保留两份 SHA。

剩余范围：macOS Intel、Windows、Linux 只有构建证据，Windows ACL、签名公证与远程 CI 未执行。不能以 Mock 或交叉编译宣布原生就绪。

## 实际新增非推理链路与停止边界

`9a3ff361fcc851d9c3080624099cbe3b92db20f6` 实现固定来源 models.dev 真实 GET、缓存/条件请求、失败保留、显式实例导入、按字段来源及公共 SHA。`6d0b367...` 补超时/超限失败状态回归。真实抓取日志包含 fetchedAt/ETag/bytes/SHA；公共目录可变化，离线回归使用保存的真实结构样本。

同一提交提供独立合成配对/只读能力测试桥及 OS 用户目录，未假装自定义配对协议是官方协议。真实 Desktop、网站上传、Cookie、自动压缩、付费模型均未开启。下一轮唯一优先任务是隔离宿主的合法只读配对，详见 `docs/NATIVE_INTEGRATION.md`。
