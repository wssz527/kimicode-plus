# 功能与验证记录

验收使用本项目 `work/` 下的隔离数据目录和独立宿主 profile（Windows 端使用独立临时隔离目录）。当前范围为核心 M0–M6、插件接口 v2（A1）、规划插件（A3）、修正轮（R0–R4）及下一轮（N0–N6）。多屏会话插件 A2 延期。

## v0.2.0 发布前复验

F1 切号回滚测试覆盖凭据恢复失败、配置恢复失败、两项均失败、两项均成功，以及成功切换但备份清理失败。保活测试确认两次 Git 状态文本相同且文件内容不同时会跳过撤回，读取无法确认时也会跳过。

F2 使用 macOS 隔离 Kimi Code 1.3.7 验证安装预览、高权限二次确认、包内容变化后的 `409 PLUGIN_PACKAGE_CHANGED` 以及重新预览时清空授权。回归测试覆盖两个插件先后挂载分栏及两种关闭顺序。Windows amd64 候选在 `wssz-win` 上实际完成 v2 ZIP 预览和安装、摘要失配拒绝、命令注册及核心调用路由。使用同一台 Windows 主机上的独立无头 Edge 打开候选内置工作台，确认真实预览数据在深、浅色下均显示三条「Windows 暂不可用」说明。

| Windows 能力 | 当前行为与复验范围 |
|---|---|
| 本地 ZIP / 文件夹安装 | ZIP 经实际核心安装成功。文件夹与 ZIP 选择器接入官方对话框及 Windows 交互桌面对话框后备。验收机控制台没有登录用户，交互选择尚未复验。 |
| 插件命令 | 命令注册、鉴权和核心路由在 `wssz-win` 通过。真实宿主内所属插件的命令回调仍待交互桌面复验。 |
| 主区整页和分栏、右侧标签、会话信息入口、右侧面板列表、同文档「+」菜单 | 运行时与挂载回归覆盖。Windows 最新候选的界面点击尚待交互桌面。 |
| 插件独立网页、独立弹出窗、独立的右侧「+」浮层 | Windows 暂不可用。核心或 Windows 桥返回 `PLATFORM_UNSUPPORTED`，预览和详情显示限制。网页与弹出窗在实际核心复验，独立浮层由桥接测试覆盖。 |

F3 自更新由独立进程监督。`runtime.json` 须指向新启动的核心，且健康接口确认目标版本后，才记录更新成功并清理备份。测试通过 `APIBaseURL` 向本地发布源读取 `scripts/package.py` 生成的真实归档和 `SHA256SUMS`。

| 实机 | 正常升级 | 新版启动即退出 |
|---|---|---|
| macOS ARM64 | `0.1.99` → `0.2.0`，原端点恢复健康，诊断状态 `updated` | `0.2.1` 退出码 17，自动恢复 `0.1.99`，原端点健康，旧二进制哈希一致，诊断状态 `rolled_back` |
| Windows x64（`wssz-win`） | `0.1.99` → `0.2.0`，原端点恢复健康，诊断状态 `updated` | `0.2.1` 退出码 17，自动恢复 `0.1.99`，原端点健康，旧二进制哈希一致，诊断状态 `rolled_back` |

双平台均确认升级后同步两份许可文件，并在新版健康或旧版恢复健康后清理二进制备份。许可文件同步失败会留在诊断记录。四组报告见 `work/release-fix-20261119/packaged-update-*.json`。隔离 macOS 宿主的深、浅色诊断页均显示「更新失败，已回滚」和启动失败原因，截图已读回。真实 GitHub Release 升级待 `v0.2.1` 发布后验证。

| 全量回归 | 结果 |
|---|---|
| Go 生产范围测试及 vet | `./cmd/... ./internal/... ./plugins/...` 全部通过 |
| Go 并发检查 | `./cmd/... ./internal/... ./plugins/...` 全部通过 |
| Web | TypeScript 类型检查通过，296 项测试全部通过 |
| 构建及发行二进制 | `make build` 通过，`scripts/acceptance.py` 27/27 通过 |
| 五平台归档 | macOS ARM64 / Intel、Windows x64 / ARM64、Linux x64 全部构建并打包，逐包校验了二进制、`LICENSE`、`THIRD_PARTY_NOTICES.txt` 和 SHA-256 |

### Windows 补验与 CI 修正

Windows 缓存扫描使用文件句柄的卷序列号和文件索引区分追加、截断及替换。`wssz-win` 已通过对应回归，最终归档再次通过第三方 ZIP 安装、命令路由、正常更新及故障回滚。Windows 测试使用真实路径的 JSON 编码，按平台验证启动器和文件权限，并等待 Node 探针关闭后自然退出。回归报告见 `work/release-fix-20261119/windows-followup/`。Windows 官方宿主的交互验收仍需已登录的桌面会话。

Windows 重启时，若有读句柄短暂占用 `runtime.json`，核心会等待并重试原子替换。持有旧文件读句柄的回归已在 `wssz-win` 复验。发行验收使用同一核心的 HTTP 连接，并在异常时记录请求、核心退出状态与标准错误。修订后的 Windows 二进制连续 20 轮通过 27 项验收，升级与自动回滚也再次通过。

## 下一轮 N0–N6 合并与隔离宿主验收（2026-11-19）

最终候选在 macOS ARM64 的隔离 Kimi Code 1.3.7 / Electron 45.1.0 上完成 N0–N6 全链路回归与深/浅色截图读回核验。预装增强插件版本为 `kimicode-plus.monitor@1.1.0`、`kimicode-plus.planner@1.0.3`、`kimicode-plus.kct@1.0.2`、`kimicode-plus.scheduler@1.1.0`（均默认关闭）。最终 `dist/enhanced-kimi`（`darwin/arm64`）SHA-256 为 `67a9b20580f685c316b1622b58f3c2880d718424014a30f1d72c00f634f36ab9`，`dist/THIRD_PARTY_NOTICES.txt` SHA-256 为 `324013f931785e5f6731318db5a6db106b6742f7e5e12ed5bd48da702ac77e84`。

| 检查 | 结果与证据 |
|---|---|
| 标准命令与构建 | 24 个 Go 包 `go test -count=1 ./cmd/... ./internal/... ./plugins/...` 与 `go vet ./cmd/... ./internal/... ./plugins/...` 全部通过；前端 `npm run typecheck` 与 284/284 单元测试通过；`make build` 与 `scripts/crossbuild.sh` 生成五平台二进制产物（`darwin/arm64`、`darwin/amd64`、`linux/amd64`、`windows/amd64`、`windows/arm64`）；`python3 scripts/acceptance.py --binary dist/enhanced-kimi` 27/27 通过。 |
| N0 收口与配置往返 | 历史遗留改动与文档同步完成，配置往返与契约测试回归通过（提交 `7a852af`）。 |
| N1 契约注入（Mac） | 移除官方版本号白名单拦截，改为安全门槛 + 11 项 DOM/API 探针按功能点降级，覆盖普通/多标签双模式侧栏及官方「账户」「智能体与会话」「实验室」内容区；隔离 1.3.7 与伪装版本探针验收及深/浅色诊断页截图通过（`work/next-round-20261119/n1-acceptance-report.json`）。 |
| N6 设置层级归位 | 「KimiCode+」标签页改为「缓存」「增强插件」「状态与诊断」三节平铺；账号、权限规则/子代理池、会话并排分别挂载至官方「账户」「智能体与会话」「实验室」页并在探针失效时回落；界面统一称「增强插件」且不注入官方「插件」页；隔离 1.3.7 深/浅色逐页截图核验通过（`work/next-round-20261119/n6-acceptance-report.json`）。 |
| N3 多账号一键切换 | macOS 隔离 1.3.7 实测同进程切换，K2.8 有额度账号正常回复，无额度账号收到官方 429。用户亲自切换三次成功。官方额度读数有延迟，有额度账号的最后可用快照早于模型请求，额度归因未得到证明。本轮未消耗真实额度补测。详见 `work/next-round-20261119/n3-real-accounts-report.json`。 |
| N4 空闲会话保活 | 供应商缓存时长推导、五态状态机、空闲回合发送后经 `:undo` 撤回、等待子代理短文案插话、保活期间禁压缩、全局缓存设置与会话信息单行菜单全部完成；隔离 1.3.7 + 合成上游逐字节前缀对比验证撤回不破坏缓存前缀，深/浅色截图通过（`work/next-round-20261119/n4-acceptance-report.json`）。 |
| N2 Windows 原生注入与跨版本兼容 | 完成 Windows 原生 CDP 注入、固定路径启动器、快捷方式备份/恢复、进程签名校验与主进程回环桥接；在 Windows 11 x64 实机完成 1.3.2（无 `.settings-dialog` 类）、从更新包隔离解包的 1.3.7（复用 Core 升级切换、探针重置、多标签侧栏切换与 DOM 故障隔离）及模拟未来版本 1.9.9 三阶段跨版本验收，并修复 Windows `kimi-code-app/1.3.x` UA 版本解析、升级探针重置、1.3.2 设置弹层选择器和 API 探针独立并发超时（`work/next-round-20261119/win-acceptance/`、`work/next-round-20261119/win-reverify/`、`work/next-round-20261119/win-cross-version/`）。 |
| N5 定时任务补强 | 完成启用时未开自动接入弹窗提醒、休眠/通知说明条与跳转官方「通用」页、`[定时] ` 无表情前缀、已读后走官方 `POST /sessions/{id}:archive` 归档、仅补缺运行失败/校验失败/被跳过且遵循官方「系统通知」开关、任务与全局成本护栏、运行前校验、提示词变量插入预览与 `yolo` 脏 Git 工作区二次确认。隔离 1.3.7 实测全链路 12 项断言通过，并修复自定义非托管供应商在 `/oauth/userinfo` 返回 401 时的误拦问题，深/浅色截图已逐张读回核验（`work/next-round-20261119/n5-final-report.json`、`work/next-round-20261119/n5-final-shots/`）。 |

## 修正轮 R0–R4 合并与隔离宿主验收（2026-11-18）

最终候选在 macOS ARM64 的隔离 Kimi Code 1.3.7 / Electron 45.1.0 上完成 R0–R4 全链路回归与截图读回核验。预装插件为 `kimicode-plus.monitor@1.1.0`、`kimicode-plus.planner@1.0.3`、`kimicode-plus.kct@1.0.2`、`kimicode-plus.scheduler@1.0.0`（均默认关闭，`example-hello` 仅作仓库模板保留、不再预装）。`dist/enhanced-kimi`（`darwin/arm64`，18,453,106 字节）与四个隔离验收目录中的二进制 SHA-256 均为 `9813494d92c2ef535f48afbd6fe2de7248db84b167054c38d7efb439c7e07455`；`dist/THIRD_PARTY_NOTICES.txt` SHA-256 为 `4522b8803fa328fa014c371a5f63701e555a87647ae2a3f543ba98990d4f0233`。

| 检查 | 结果与证据 |
|---|---|
| 标准命令与构建 | 22 个生产 Go 包 `go vet` 与 `go test -race` 全部通过；前端 `npm run typecheck` 与 258/258 单元测试通过；`scripts/build.sh` 与 `scripts/crossbuild.sh` 生成五平台产物，`python3 scripts/acceptance.py --binary dist/enhanced-kimi` 27/27 通过。 |
| 主会话回归修复 | 递增 `planner`（`1.0.2 -> 1.0.3`）与 `monitor`（`1.0.1 -> 1.1.0`）版本号，修复存量数据目录升级时跳过文件替换及官方摘要失配问题；修复 `web/src/native.ts` `createEventChannel` 在长轮询挂起期间新增订阅作用域的 8 秒盲区与丢事件竞态，并补充双端回归单测。 |
| R1 入口挂载与平滑升级 | 隔离环境启用插件：`kimicode-plus.planner`（测试结束停用）。保留存量 `1.0.2`/`1.0.1` 目录直接启动完成平滑升级，10 步实机检查全通：停用态零残留，运行期启用 115 ms 内同步注入“会话信息 → 可打开”、右侧空白面板列表与 `+` `browser-overlay` 菜单，窄窗 `<960px` 浮动模式点击正常，停用 108 ms 内彻底移除（`work/correction-20261118/r1-stage/r1-acceptance-report.json`）。 |
| R2 模型能力、子代理池与凭据 | 隔离环境启用插件：无。官方“模型供应商”模型行打开“能力与限制”修改图片/工具能力、思考模式三段、自适应思考、最大输出 32K 与推理强度，保存读回并保留原密钥，重启宿主后持久生效；子代理池下拉从官方目录读取并写回 `secondary_model`；插件详情页凭据添加、替换、删除且零明文回显；独立 `/models`、`/credentials` 页面与管理窗口跳转行已移除（`work/correction-20261118/r2-stage/r2-acceptance-report.json`）。 |
| R3 余额数据层与 Monitor 复刻 | 隔离环境启用插件：`kimicode-plus.monitor`（测试结束停用）。上游桌面版模块化面板、Rive 蓝球形象三态（空闲呼吸/思考计时/调用计时）、长按三区拖拽排序与迷你模式全部通过；5h（56%）与每周（44%）额度条、加油包（`¥19.75`）及外部账户（DeepSeek `¥20.50`、智谱 BigModel `35.0%`）正常渲染，读取失败不显示为 0，7 项密钥泄露审计均为 `false`（`work/correction-20261118/r3-stage/r3-acceptance-report.json`）。 |
| R4 定时任务调度与插件 | 隔离环境启用插件：`kimicode-plus.scheduler`。默认停用零注入，启用后挂载左侧栏“新建会话”下方与会话信息入口，主聊天区 `mode: "page"` 全屏任务页与返回聊天切换正常；每分钟测试模式自动触发新建会话、绑定会话忙碌等待完成、无人值守审批不自动批准（标记“等待批准”并增加未读计数）、停用清理及卸载保留/删除数据均验证通过（`work/correction-20261118/r4-stage/r4-acceptance-report.json`）。 |

深浅色对比截图已在主会话读回核验。本轮所有模型回合均使用隔离环境的本地合成服务，未操作日常客户端实例、未调用真实付费模型推理。Windows 上已验证范围仅限独立工作台和后端接口（Windows 原生界面注入当时未启用）；规划插件已扩展至 8 个站点（新增豆包、通义千问、元宝）及模型归纳后端，8 个站点的真实登录提取与回填尚未验证。

## 1.3.7 兼容与规划返修（2026-11-18）

最终候选在 macOS ARM64 的隔离 Kimi Code 1.3.7 / Electron 45.1.0 上通过版本、DOM 和运行时检查。预装规划升级为 1.0.2，仍默认关闭。`dist/enhanced-kimi` 与隔离验收副本的 SHA-256 为 `75122812c3a34fb4031c5d9f0b8fa39b859bbc8dfb5c14f2a0dee5dfe8984b79`。

| 检查 | 结果 |
|---|---|
| 标准命令 | `make check`、`make test`、`make build` 通过，前端 247/247 |
| 二进制 | `python3 scripts/acceptance.py --binary ./dist/enhanced-kimi` 通过，28/28。`make crossbuild` 生成五个架构产物，原生运行验收覆盖 macOS ARM64。 |
| A3 隔离宿主 | 本地流程 14/14。实际右栏指针拖拽从 719px 调到 607px，关闭规划后恢复官方标签。新建规划后可从历史重新打开原线程。新旧 Code 会话均收到交接文本。 |
| 发送与回填回归 | 异步发送仅创建一次，保留后续编辑的草稿。交接成功会消费确认。回填的合成 DOM 用例覆盖页面切换、输入框替换、草稿变化和写入后读回。 |
| 插件与视图清理 | Monitor 与规划共用一条事件轮询，原生子视图跨窗独立，并发打开复用同一视图，停用时中断加载并释放资源。 |
| 缓存观察 | 保活关闭时可在原生设置切换模型。合成样本分别显示 60% 和 25%，各 1 次，见 `cache-selection-report.json`。分模型只纳入同一会话连续使用同一已知模型的主代理请求。真实收益仍需有无保活对照。 |

隔离证据见 `work/acceptance-137-final-J2YjUV/` 下的 `a3-acceptance-report.json`、`host-regression-report.json`、`view-lifecycle-report.json` 和深浅色截图。五站均可导航，Claude 进入安全验证页，DeepSeek 显示环境提示。五站登录后的提取、交接和回填仍待人工验收。真实付费模型的保活收益与日常实例也仍待验证。

## 上一候选验收（1.3.5，2026-11-18）

范围：核心 M0–M6、插件接口 v2（A1）和规划插件 A3；A2 多屏会话插件延期。规划预装版本为 1.0.1，默认关闭。旧内置规划入口、面板、弹窗动作和演示工作台已移除，旧多屏路径及其数据保留。`Makefile` 与 README 的生产检查范围统一为 `./cmd/... ./internal/...`。

| 检查 | 当前结果 | 证据 |
|---|---|---|
| 标准检查 | `make check`、`make test` 通过；前端 246/246，无跳过 | 生产 Go 测试、vet、TypeScript 与现有前端测试 |
| 并发检查 | desktop、plugins 的 race 检查通过 | `go test -race ./internal/desktop ./internal/plugins` |
| 构建与二进制 | 完整构建通过，二进制验收 28/28 | `scripts/build.sh`、`scripts/acceptance.py` |
| 平台构建 | macOS ARM64/Intel、Windows x64/ARM64、Linux x64 均编译成功 | `scripts/crossbuild.sh`；仅 macOS ARM64 原生运行 |
| A3 完整宿主流程 | 14/14 本地阶段通过 | `work/a3-stage-final/a3-acceptance-report.json` |
| 交接确认 | 6 类字段变化立即清除确认；预览在途切换目标、乱序响应和重复提交均受控 | 前端回归、A3 第 10 阶段的实际界面与合成官方请求 |
| 交接生效模型 | 新旧会话均实际收到文本，原有会话模型保持 synthetic-official | A3 报告 `checks.handoff` 与合成官方请求记录 |
| 慢加载关闭 | 关闭立即中断归属当前令牌、实例和窗口的视图，不再等待页面响应；同一视图并发打开不重复创建 | `work/a3-stage-final/view-lifecycle-report.json`；修复前复现保留在 `view-lifecycle-before-fix.json` |
| 相邻宿主回归 | 设置六行导航、仅保留会话并排旧开关、Monitor 与规划共存一条事件轮询、同 viewId 跨窗隔离及停用后零节点/轮询/侧栏占用均通过 | `work/a3-stage-final/host-regression-report.json` |
| 视觉 | 官方侧边聊天已实际打开；规划本地深浅色与网页子视图截图已读回 | A3 报告 `screenshots`；主 WebContents 不包含子视图，二者分别取证 |
| 依赖审计 | 生产依赖 0 项；开发依赖链 4 项 high 告警 | `npm audit --omit=dev`、`npm audit`，见下方限制 |

最终 macOS ARM64 二进制 `dist/enhanced-kimi` 与宿主验收副本的 SHA-256 均为 `09bb001f193580008f29bc7b1c1c64d321f4738db1c5edf5b8f0142fe4db9457`。A3 报告同时记录规划源码、清单、宿主注入源码和 host bundle 的 SHA-256。

核心缓存、KCT 和 M6 未被本轮重写，复用未受影响的阶段证据：`work/kct-stage-acceptance/evidence/core-coexist.json`、`kct-lifecycle.json`，`work/m6-stage-acceptance/evidence/m6-config.json`、`m6-restart.json`，以及 `work/a1-stage/evidence/` 中对应权限、归属、附件和清理记录。这些历史证据不冒充对当前候选重新执行的完整实机测试。

### 五站实际状态

| 网站 | 可见网页视图打开 | 登录后提取 / 交接 / 回填 |
|---|---|---|
| Kimi | 已打开，显示登录入口 | 未验证，需用户登录 |
| DeepSeek | 已打开至 sign_in，页面还提示使用环境异常 | 未验证，需用户登录并确认站点环境要求 |
| Gemini | 已打开，显示登录入口 | 未验证，需用户登录 |
| Claude | 已打开新对话页，隔离 profile 未确认登录 | 未验证，需用户登录 |
| ChatGPT | 已打开，显示登录入口 | 未验证，需用户登录 |

五站适配器有合成 DOM 单测；本地合成网页已验证选区提取、编辑预览与真实 KAP 交接。它们不代表五站真实账号全流程通过。程序不会绕过验证码，不在网站内自动发送。

### 剩余边界

真实付费模型、真实账号自动保活/压缩/恢复、其他平台原生运行、系统文件选择窗口 GUI 点选、签名公证与完整窗口合成截图仍未验收。合成上游的缓存 verified 回执只证明归属判定，不能证明延长真实缓存 TTL。A2 插件按用户决定延期。

开发工具链告警均追溯到 [picomatch 正则拒绝服务](https://github.com/advisories/GHSA-fc5q-39xc-vjpf)，涉及 picomatch、fdir、tinyglobby 和 Vite 的依赖传播。当前 npm 的自动修复建议包含 Vite 主版本回退，本轮未修改锁文件或执行强制回退。该问题保留为构建环境的已知依赖风险，生产依赖审计结果为 0。

验收期间一次测试 Skill 路径配置错误、一次 inspector 首次读取超时均保留了失败报告。修正夹具及确认宿主就绪后，同一最终构建完成了上述验收；没有通过删断言或把外部未验项目改成通过来收口。

## 缓存与额度（M2，2026-11-16）

数据来自本机 `~/.kimi-code/sessions` 的只读解析，全部验证未改动官方文件、未调用付费模型。

| 范围 | 结果 | 证据 |
|---|---|---|
| 增量读取 | 追加、截断、同路径替换（inode 变化）、半行不解析且下轮补全、5 秒扫描下限均通过 | `internal/cache/cache_test.go` |
| 超长行 | 官方记录存在单行约 360KB。早期实现只吃掉首个读缓冲，21.8MB/263 步的会话入库仅 14 步；修复后入库 257 步。回归测试 `TestReadsPastOneBuffer` | `internal/cache/cache_test.go` |
| 模型关联 | `usageScope=turn` 才参与配对，按 usage 四字段完全相等匹配；配对失败记 UNKNOWN；usage 为空的 error/interrupted 不入库 | `TestModelPairingAndUnknown` |
| 速度字段 | 解码时长取 `llmServerDecodeMs`，用 `firstToken>stream` 的样本断言不是 `stream-firstToken` | `TestSpeedUsesServerDecode` |
| 未上报判定 | read/creation 双零记 NOT_REPORTED 且不显示 0%；有非零 read 的真实低命中（3%）给数字 | `TestNotReportedVersusLowHitRate` |
| 命中率与保留时长 | 命中率公式、retention 按空闲时长分桶均通过 | `TestHitRateFormula`、`TestRetentionBuckets` |
| 保活时钟 | "距上次请求"只跟主代理，子代理更新的步骤不刷新 | `TestSessionsIgnoreSubagentActivity` |
| 保活策略 | 子代理活跃时仍触发、审批阻断、空闲不触发、无后台子代理不触发、阈值以下不触发、阻塞等待记 skipped | `internal/desktop/cache_keepalive_test.go` |
| 保活闭环判定 | cacheRead>0 判成功；双零判 unknown（不伪造）；有上报但命中 0 判无效；窗口内被后续保活接手时记 invalid 而不是冒领对方步骤 | `TestJudgeKeepAliveVerdicts`、`TestSupersededKeepAliveDoesNotClaimTheNewerStep`、`TestSupersedeQueryScopesToSession` |
| 真实会话手算 | 3 个会话与手算一致（含 931657/0.990900 的重合） | 见下方实测记录 |
| 前端 | typecheck 通过、81 项测试全绿、build 成功 | `web/` |
| 真实二进制端到端 | 隔离宿主上 `cache.summary`/`sessions`/`history`/`retention`/`receipts` 返回真实值；`monitor.summary` 已删除，调用返回未知动作；阈值 5–60 校验生效；设置写入读回一致 | 隔离宿主运行日志、`TestMonitorSummaryActionIsGone` |
| 保活自动闭环 | 隔离宿主上由调度器自动触发，两次连续保活分别落 `invalid` 与 `verified`，回执在页面上按时间排列可见 | 见下方"保活闭环实测" |
| 页面截图 | 深浅两种配色均渲染正确：四张指标卡并排、会话表格三列、策略开关、留存条、实际用量柱状图、最近执行记录三种回执状态 | `work/ab/shots/cache-light.png`、`cache-dark.png` |

### 保活闭环实测（隔离宿主，合成上游）

前提状态：主代理已派发 `run_in_background: true` 的后台子代理并结束回合，官方报 `busy=true / main_turn_active=false`，`cache_live.open_background=1`，主代理上一次模型请求在 30 分钟前。

观察到的完整序列：

| 时刻 | 事件 |
| --- | --- |
| 13:18:07.295 | 调度器自动发送保活 #1，意图转 `acknowledged` |
| 13:18:07 – 13:23:07 | 该会话没有产生任何主代理步骤 |
| 13:23:07.295 | 冷却结束，保活 #2 发出 |
| 13:23:07.359 | 主代理产生新步骤，`input_cache_read=1100` |
| 13:23:07.505 | 保活 #1 落 `invalid`："本次保活窗口内没有产生自己的模型请求，后续保活已接手核对，本次保活无效" |
| 13:23:22.370 | 保活 #2 落 `verified`："主代理下一步命中缓存，input_cache_read 大于 0，保活成功" |

首轮实现里保活 #1 会把保活 #2 引起的步骤算成自己的成果、写成 `verified`。本轮修复后两者归属分明，变异测试（删掉该分支）会复现原始的 `verified/1100` 错判。

限制：本次合成上游在保活请求上返回 `cached_tokens=1100`，所以 `verified` 证明的是"闭环能把下一步的 `input_cache_read` 正确归属到对应的保活"，**不证明**保活真的延长了官方缓存的存活时间。后者需要同会话有/无保活的中断对比，本轮未做。

### 隔离宿主 A/B 实测（D8 发现阶段）

环境：`../work/ekd-native-acceptance` 隔离宿主（官方 2.6.0，独立 `KIMI_CODE_HOME` 与独立 Electron profile），合成 OpenAI 兼容上游记录每次请求并上报 `cached_tokens`。provider `base_url` 经官方 `/config` 热改并在结束后还原。未接管日常实例、未安装真实 LaunchAgent、未调用付费模型。

- **状态 A（后台等待）可保活**：首条消息产生请求 → 回合结束 `busy=false` → 保活消息 HTTP 200 → 上游记录到携带保活标记的新请求（4 次请求中 2 次命中标记）。
- **状态 B（阻塞等待）不可保活**：合成上游返回真实 `Bash` 工具调用执行 `sleep 75`，阻塞窗口内发送保活消息被接收，但阻塞期间与工具结束后均**没有**新增请求，保活标记未出现在任何请求里。实现因此记 `skipped` 并说明原因。

方法论修正：最初通过改写 `config.toml` 指向合成上游，上游收到 0 个请求——宿主只在启动时读配置。必须用官方 `/config` 热改才能验证。

## 历史原生聚合验收（2026-11-16，插件化前）

| 范围 | 当前结果 | 证据与限制 |
|---|---|---|
| 正式宿主连接 | 隔离 Kimi Code 1.3.5 / Electron 45.1.0 的版本、DOM revision 1、RPC 来源与核心读取检查通过 | 主实例未操作；当前返修脚本为 `../work/ekd-final-integration/verify-review-fixes.mjs`，旧 `verify-native-ui/flow.mjs` 仅保留早期记录 |
| 核心与界面恢复 | 核心重启、旧连接清理、入口去重、空会话挂载和官方单会话恢复已验证；Vue 重建后原生子节点不漏出；原生浏览器已打开时重连仍恢复侧栏 | `verify-review-fixes.mjs`；最终构建的 `final-native-smoke.mjs` 在工具模板数量为 0 时验证 Monitor/设置入口、默认网页规划和设置列表，`final-native-smoke-result.json`；`final-native-settings.png`、`final-native-sidebar.png` 已读回 |
| 原生界面 | 深浅色、760px 设置窄窗、900px 三窗格、双/三窗格输入区无重叠通过；统一规划入口默认网页 | `review-fixes-result.json`、`review-multi-dark.png`、`review-multi-light.png`、`review-multi-narrow.png`；无常驻工作台/选择器/空闲状态带 |
| 本地规划 | 合成供应商下真实流式、停止、write 审批、多选与编辑交接通过 | `verify-planner-flow.mjs`、`review-planner-flow-dark.png`；批准前未写文件，批准后读回，Code 真实接收文本，未暗改默认模型 |
| 多会话 | 同一入口从两窗格追加到三窗格，侧栏切换主窗格、已显示会话交换、草稿保留；并行更新、独立停止、模型/模式与 Read 审批通过 | `verify-review-fixes.mjs`、`verify-multi-session.mjs`、`review-multi-session-dark.png`；权限规则在 finally 恢复 |
| 附件与消息 | 点加号、附件条目、权限选择、真实 KAP 文件发送、消息时间及引用回复通过 | `verify-attachment-flow.mjs`、`review-attachment-result.json`；文件对话框返回值使用确定性替身，未实际 GUI 点选系统文件窗口，后续 KAP 发送和消息读回为真实调用 |
| 网页到 Code | 合成网页真实 WebContents 选区提取、编辑预览、已有会话交接与回复通过 | `verify-web-flow.mjs`、`review-web-flow-view.png`；可见性、弹层遮挡与坐标核对通过，不证明网站内自动聊天 |
| 设置聚合 | 单层 5 项列表加独立管理窗口跳转行嵌入官方设置，旧多层导航与硬嵌页面移除；标签顺序、原生标签恢复、缓存撤销/默认/保存读回通过 | `verify-review-fixes.mjs`；`verify-settings-flow.mjs` 验证布局、网站权限保存后恢复和真实模型目录；`review-settings-list-dark.png`、`review-settings-models-dark.png`、`review-settings-narrow-light.png` |
| 恢复面板与 Monitor | 手动监听、检查、撤销和官方单会话恢复通过；Monitor 默认约 233px，详情折叠，无样本显示未知 | `verify-recovery-flow.mjs`、`review-sidebar-dark.png`；自动化测试期间暂关并恢复原值，不把隔离配置的 recovery.enabled=true 说成默认值 |
| 自动化执行 | 五秒调度、保活、compact ACK/完成、额度窗口、Goal 阻断、撤销及重启 UNKNOWN 均有合成 KAP 回归 | `internal/desktop/automation_test.go`；产品三项默认关闭，未启用真实账号自动动作 |
| 前端静态检查 | TypeScript 类型检查、61 项测试及完整 build 通过 | `web/` 当前源码和 `scripts/build.sh`；含按官方会话切换时不重复窗格的回归 |
| Go 与二进制 | 生产包测试、desktop/chat race、vet 通过；二进制 21/21 验收通过 | `go test ./cmd/... ./internal/...`、`go test -race ./internal/desktop ./internal/chat`、`python3 scripts/acceptance.py --binary ./dist/enhanced-kimi` |

本轮没有调用真实付费模型、真实网站账号或真实浏览器 Cookie。自动化三项产品默认关闭；实机恢复脚本暂关自动化并在结束后恢复隔离环境原值，不能把合成调度测试说成真实账号自动化验收。规划、多会话、设置和恢复面板的主视图截图已读回，网页子视图截图也已读回；主 WebContents 截图不包含子 WebContentsView，当前录屏权限未取得完整窗口合成截图，不能据此声称具有完整窗口视觉证据。

UI 机械检查最初报告两项：Markdown 引用块左边线是语义样式而非装饰卡片，保留；Monitor 进度条的宽度动画已移除。机械检查不能替代上述实机操作与截图检查。

## 0.2.0 历史基线

以下记录保留早期独立核心、模型、用量和设置挂载的历史结果。其中的“未完成”只描述当时基线；与当前原生状态的关系以 `NATIVE_INTEGRATION.md` 为准。

## 审查项处理

| 项目 | 本轮处理 | 证据 |
|---|---|---|
| F01/F02/F04/F05/F06/F07/F08 | 继承审查已关闭的配置、附件、派发、用量、目录及证据修复，不重复重写 | 原生产回归和二进制场景继续通过；旧审查包保留在 work/ |
| F03：模型抽屉乱序 | 请求代次绑定当前模型，关闭/卸载使旧响应失效；实体与 revision 一起固定 | web/tests/async-target.test.ts，第一个生产处理函数回归 |
| F03：公共导入错目标 | 预览绑定来源/目标，请求代次与当前选择复核；改选清空预览，保存核对目标 | 同文件第二个回归；实际公共导入及页面读回 |
| F09：宿主体验 | 设置区嵌入、子页返回、主题变量、上下文回调与可装配 ES 模块已实现 | 隔离 Kimi Code 1.3.4 的真实设置页挂载、核心读取、标签还原、关闭重开与重复注入；正式 IPC/常驻产品接入仍未完成，最新 embed 版尚无可用系统截图 |
| 本轮发现的主题覆盖 | 深色主题选择器覆盖内层 ekd-root，修正提示文字颜色和标题选择器 | 最终 Go 内嵌页面深浅模式实际查看 |

F03 回归通过 TypeScript AST 抽取生产处理函数并执行受控异步请求，不等同于 React DOM 或跨浏览器端到端测试。前端原直方图测试使用过期固定日期，改为当天 UTC 夹具；生产用量计算未改变。

## 实际功能增量

- AES-256-GCM 加密凭据库，口令派生、锁定、secretRef；无明文读取 API。
- Go 供应商模型目录及四种轻量聊天协议，稳定请求意图、12 条上下文、持久请求状态与用量；UNKNOWN 不自动重发。
- 本地会话引用、Worktree、布局/Reviewer 偏好、分层压缩条件评估。
- 用户导出 JSON 的站点/全部范围、Cookie 目标冲突、加密隔离保存及独立网站权限。
- 可信 Go 指令整理和 Monitor 兼容用量导入，无独立 Node 服务。
- 真实可操作的二级页面及可装配宿主界面模块；隔离 Kimi Code 设置页的运行时入口已验证，正式 IPC 与常驻产品接入尚未完成。

- 用户显式路径的只读凭据导入、Unix 秒 expires_at 预览、供应商账号 revision/启停/优先级与默认停用边界。
- 轻量聊天 UNKNOWN 的显式核对解除；INFLIGHT 不可解除，审计不改写、不自动重发。
- 独立本机令牌保护的 OpenAI/Kimi 网关，支持流式/非流式、reasoning_content、Kimi 扩展字段、工具调用回环、用量与账号归因。官方 Kimi CLI 2.4.1 已在隔离 Home 中通过合成上游完成文本和真实 Read 工具回合。

## 2026-11-13 增量复验

- Go：`go vet ./internal/... ./cmd/...` 与 `go test ./internal/... ./cmd/... -count=1` 覆盖全部生产包；网关含真实二进制、工具分片、流完整性、上游错误和整流 dispatch 锁回归。
- 前端：TypeScript 类型检查通过，57/57 测试通过；最终 `scripts/build.sh` 的普通 Vite、host 模块、Go 内嵌构建与依赖通知均成功。
- 最终二进制：`python3 scripts/acceptance.py --binary ./dist/enhanced-kimi` 21/21 通过。真实 CLI 与原生设置页复验均使用该最终 `dist/enhanced-kimi`，不是只验证临时构建。
- 真实 CLI：`../work/ekd-gateway-acceptance/result.json` 为 PASS。直接 SSE 证据含推理、stop、纯 usage 和 `[DONE]`；`cli-tool.stdout.jsonl` 含 Read 调用、实际文件内容、相同 tool_call_id 和 EKD_TOOL_ROUNDTRIP_OK。所有账号、密钥、上游和配置均为隔离合成数据。
- 原生设置页：最新 embed 版的标签样式、隐藏旧顶栏、核心读取、模型导航、原生标签恢复、主题同步、关闭重开、重复注入及 Core 重启后重新配对均通过。系统截图仍不可用，未宣称最新视觉截图验收。

## 原始自动化输出

日志路径相对项目根目录。没有将旧版本结果充当本轮结果。

| 检查 | 结果 | 日志 |
|---|---|---|
| Go 生产范围 go test -v ./cmd/... ./internal/... | 191 顶层、61 子测试通过；1 个显式联网测试跳过 | work/go-core-test.log |
| 最后一次聊天派发修改后的相关回归 | chat/server 5 顶层、7 子测试通过 | work/final-chat-server.log |
| go vet ./cmd/... ./internal/... | exit 0，无诊断 | work/go-vet.log |
| F03 两个异步目标回归 | 2/2 通过 | work/f03-regression.log |
| 前端测试 | 53/53 通过 | work/frontend-final.log |
| 最终 TypeScript 类型检查 | exit 0 | work/typecheck-0.2.0.log |
| 完整 scripts/build.sh | 普通 Vite、host 模块、内嵌 Go、依赖通知成功 | work/build-0.2.0.log |
| 最终发布二进制 | 21 组通过 | work/binary-final-0.2.0.log |
| Git Worktree 实际 HTTP 调用 | 临时仓库 detached 创建、文件读回及清理通过 | work/worktree-0.2.0.log |
| 五平台交叉构建 | exit 0 | work/crossbuild-0.2.0.log、work/platform-sizes-0.2.0.json |
| UI 机械检查 | 空 findings | work/ui-detector-0.2.0.json |

首次 go test ./... 也扫描到 work/review-0.1.1/return/evidence/ 中依赖旧测试私有函数的孤立探针，导致该目录编译失败。它不是生产包，未删除旧证据；后续命令显式限定 cmd/internal。不能把整个仓库递归测试称为通过。本轮未重新执行 race 检查。

最后的聊天回归覆盖凭据密文、重启锁定、四协议本地 HTTP、请求幂等、空响应 UNKNOWN、不能换 intent 重发、实际响应用量入账、Cookie 来源 storeId 冲突、压缩条件与可信插件。所有供应商响应来自 httptest，不调用真实模型。

## 界面与公开元数据

本轮使用内置浏览器直接访问最终 Go 服务，未启动额外浏览器运行时。

- 真实读取 https://models.dev/api.json，显式将 moonshotai 映射到独立本地 kimi 实例，预览并导入 6 个模型。抓取时间为 2026-11-11T01:46:38.667996Z，不依据模型名称猜测能力。
- 模型显示名改为本地别名，保存读回；最终服务重启后仍保留相同名称与目录。
- 在 1280×720 实际查看最终构建的深浅模型页面和返回设置入口。
- 会话引用通过表单保存，列表、项目和布局区读回一致；不代表官方真实会话。
- 从一条明确标记 MOCK 的独立历史中只选一条用户消息，进入交接页并成功调用 Go 脱敏预览。返回任务包保留相同消息 ID、来源线程、目标本地会话和项目。页面明确“未发送”，原生提交按钮禁用，Outbox 仍为 0。
- 最终主题截图保留于 work/ui-dark-models-0.2.0.png 与 work/ui-light-models-0.2.0.png。

独立界面截图不证明宿主兼容性。早期运行时挂载曾保存真实窗口截图 work/ekd-native-acceptance/settings-screen.png，但它是隐藏 EKD 顶栏之前的旧界面，不能作为最新 embed 版视觉证据。最新嵌入版已通过真实设置页的 DOM、计算样式、核心读取与生命周期验收；当前 macOS `screencapture` 连探测区域都返回 `could not create image from rect`，因此最新视觉截图仍未取得。实际交接仍受 Mock 边界限制。网页入口只导航，不执行网站操作。

## 平台、体积与性能

| 平台 | 二进制字节 | 本轮状态 |
|---|---:|---|
| macOS ARM64 | 14,227,314 | 原生运行、内嵌界面、21 组二进制验收、性能 |
| macOS Intel | 14,974,048 | 交叉编译成功，未原生运行 |
| Windows x64 | 15,033,344 | 交叉编译成功，未原生运行，ACL 未验 |
| Windows ARM64 | 14,005,248 | 扩展目标交叉编译成功，未原生运行 |
| Linux x64 | 14,909,600 | 交叉编译成功，未原生运行，官方 Desktop 兼容性另验 |

macOS ARM64 单二进制约 13.57 MiB；gzip 估算 6,430,024 字节，不是安装包。发布程序不包含 Node/Rust/Electron/Chromium。系统浏览器、用户数据和可选 Git 另计，未签名、公证或远程发布。远程 CI 未执行。

macOS 26.6.2 / Apple Silicon，Go 1.27.3。五个新独立数据库；进程启动至鉴权健康就绪，10 ms 轮询，OS 文件缓存已热；启动后 1 秒按 PID 测核心 RSS，不含浏览器。不是冷启动、长期内存或压力测试。

| 指标 | 中位数 | 范围 |
|---|---:|---:|
| 就绪 | 27.51 ms | 24.06–30.33 ms |
| 空闲 RSS | 18.38 MiB | 18.25–18.61 MiB |
| 关闭 | 4.70 ms | 3.70–6.85 ms |

原始轮次：work/performance-0.2.0.json、work/measure-0.2.0.log。

## 历史基线的未完成项（0.2.0）

真实付费模型、真实网站内对话/上传、系统文件选择窗口 GUI 点选、真实账号自动保活/压缩/恢复、真实速度与完整缓存观测、登录态恢复、其他平台原生运行、签名公证及完整窗口合成截图仍未验收。旧通用 Outbox 仍是 Mock；真实 KAP 文本交接与本机文件发送、三会话、内嵌设置与受控调度已通过各自范围，但不能替代真实账号或全平台验收。`go test ./...` 还会扫描 `work/review-0.1.1/return/evidence/` 中依赖旧私有测试函数的孤立探针，因此生产测试必须显式限定 `./cmd/... ./internal/...`；该历史证据包不属于产品包，未修改或删除。
