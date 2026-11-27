# 功能与验证记录

验收使用本项目 `work/` 下的隔离数据目录和独立宿主 profile（Windows 端使用独立临时隔离目录）。当前范围为核心 M0–M6、插件接口 v2（A1）、规划插件（A3）、修正轮（R0–R4）、下一轮（N0–N6）以及 v0.2.4 以来的发布修复。多屏会话插件 A2 延期。

## v0.2.13 修复多路由模型能力匹配与 macOS 关闭自动接入重开体验（2026-11-28）

核心发布版本为 `0.2.13`，标签为 `v0.2.13`（提交 `85356cf`）。本次仅更新核心，四个官方预装插件（KCT `1.0.9`、Planner `1.0.8`、Monitor `1.1.3`、Scheduler `1.1.1`）与独立通道 `plugins-latest` 保持不变。

### 交付范围

- 多路由模型能力归属匹配与 `config.get` 自动重试：后端 `internal/desktop/provider_models.go` 与 `internal/desktop/planner_stream.go` 优先按条目内 `provider` 与 `model` 归属匹配，避免模型键前缀与条目 `provider` 字段不一致时跨供应商串并或误判；前端 `provider_enhance.js` 抽取统一身份解析逻辑，并在 `config.get` 瞬时失败时执行有限次自动重试（最多 3 次，关闭表单或卸载时清理定时器）。
- macOS 关闭自动接入平滑交接：核心 plist 启用 `AbandonProcessGroup`；从运行中的日常常驻核心关闭「随 Kimi Code 启动自动接入」时，先写入交接状态并通过 LaunchServices 启动脱离 `launchd` 的接管核心，交接失败中止卸载、成功后才移除登录项，保持当前 Kimi Code 会话不断连。
- macOS 轻量手动启动入口：新增 `internal/desktop/launcher_darwin.go`，在启用或关闭日常自动接入时自动维护 `~/Applications/KimiCode+.app`（隔离 `--host-profile` 不写入日常启动器），并在设置页「状态与诊断」展示路径，方便关闭自动接入或重启系统后随时从聚焦搜索或访达重新启动并接入；同步更新 `README.md` 与 `docs/NATIVE_INTEGRATION.md`。

### 验证与复审

| 项目 | 结果 |
|---|---|
| 前端 | TypeScript 类型检查、431/431 单测、普通与宿主生产构建通过 |
| Go | `sh scripts/build.sh`、`go vet ./cmd/... ./internal/... ./plugins/...` 与 `./cmd/... ./internal/... ./plugins/...` 全量测试通过（含新增多路由匹配、规划器路由与 `launcher_darwin` 单测） |
| 打包与同步 | `scripts/test_package.py` 11/11、`scripts/test_sync_cnb_release.py` 7/7 通过 |
| 真实二进制 | `scripts/acceptance.py --binary ./dist/enhanced-kimi` 27/27 通过 |
| 本机联调（Kimi Code `1.5.1`） | 运行中常驻核心实测 `attach.status` 返回 `launcherPath` 且 `providers.modelSuggestion` 对跨前缀多路由供应商正确匹配与隔离；`KimiCode+.app` 启动脚本通过 `/bin/sh -n` 语法检查 |
| 线上发布核验 | 核心 `v0.2.13` 为 GitHub 正式 Latest（`isDraft: false`、`isPrerelease: false`）。GitHub 与 CNB 的固定清单均返回 HTTP 200，六个核心资产（`manifest.json`、`SHA256SUMS`、三平台压缩包、`THIRD_PARTY_NOTICES.txt`）逐字节一致且均小于 128 MiB。客户端 `updater` 启用 `RequireManifestSignature` 强制验签模式，在 CNB 主源（三平台 `darwin/arm64`、`darwin/amd64`、`windows/amd64`）、CNB 模拟故障回退 GitHub 源及直连 GitHub 源下均通过 Ed25519 签名校验并返回 `available` / `canApply: true`；三平台 SHA-256 与归档结构检查通过，下载的 macOS ARM64 二进制实测报告 `0.2.13`。`plugins-latest` 独立通道双源清单（7362 bytes）保持未变、逐字节一致，且通过内置公钥 Ed25519 验签（4 个插件版本未变）。 |

### 发布与后续边界

核心已通过 `v0.2.13` 注释标签触发 `.github/workflows/release.yml` 完成发布。工作流将完整资产上传至 GitHub 草稿，同步 CNB 后公开 GitHub 正式 Release。[核心发布工作流](https://github.com/wssz527/kimicode-plus/actions/runs/46038358746) · [v0.2.13 Release](https://github.com/wssz527/kimicode-plus/releases/tag/v0.2.13)

本轮为避免打断桌面会话，未在真实图形界面中点击关闭自动接入开关或双击 `KimiCode+.app` 执行交互实测，该路径由单元测试、脚本语法检查与运行态接口联调覆盖。

## v0.2.12 适配 Kimi Code 1.5.0/1.5.1 与供应商能力配置增强（2026-11-27）

核心发布版本为 `0.2.12`，标签为 `v0.2.12`。本次仅更新核心，四个官方预装插件（KCT `1.0.9`、Planner `1.0.8`、Monitor `1.1.3`、Scheduler `1.1.1`）与独立通道 `plugins-latest` 保持不变。

### 交付范围

- Kimi Code `1.5.0` 与 `1.5.1` 宿主适配与登记：恢复模型供应商编辑页（`.pf-nav` / `.pf-head-sticky`）能力与限制 UI 和开窗快照，过滤 `AddProviderPage`（`.ap`）与 `v-show` 隐藏表单，零污染添加页；将 `1.5.0` 与 `1.5.1` 登记至 `SupportedHosts`。
- 供应商模型目录补全与导入保护：后端 `FindPublicModels` 与 `providers.modelSuggestion` 支持批量查询与真实 `max_context_size` 映射；工具栏新增一键「按目录补全」批量回填 `models.dev` 真实能力、上下文长度与最大输出并同步刷新官方输入框；拦截 `POST /api/v1/providers:import_catalog` 后紧随的非编辑态覆盖 `PUT /api/v1/providers/{id}`，经 `preserveCatalogImport` 完整保留导入的扩展字段及缺省的 `base_url` / `default_model`。
- 模型能力行内抽屉界面复原与交互细化：恢复每行同排「能力与限制」行内抽屉、胶囊选项与快捷长度预设，原地承载「填入目录建议」与「批量能力模板」抽屉，对齐五列表头与输入框高度，修复十进制上下文长度（如 `128000` / `256000`）误折算与单模型目录建议未清除误加 `thinking` 的问题。
- 设置页面定位与侧栏折叠回退：修复「智能体与会话」页在 `1.5.0+` 内嵌 `v-show` 容器下的增强区块定位，对 `.settings-subpage`（如全局 `AGENTS.md`、字体子页）统一避让并在返回后恢复原位，修复宿主渲染层在窗口不可见或子页切换后 `ensure()` 停止触发的问题；补齐侧栏折叠/展开按钮的多布局与中英文标签回退。

### 验证与复审

| 项目 | 结果 |
|---|---|
| 前端 | TypeScript 类型检查、430/430 单测、普通与宿主生产构建通过 |
| Go | `go vet ./cmd/... ./internal/... ./plugins/...` 与 `./cmd/... ./internal/... ./plugins/...` 全量测试通过 |
| 打包与同步 | `scripts/test_package.py` 11/11、`scripts/test_sync_cnb_release.py` 7/7 通过 |
| 真实二进制 | `scripts/acceptance.py --binary ./dist/enhanced-kimi` 27/27 通过 |
| 实机宿主验收 | 隔离 profile 对接本机 Kimi Code `1.5.0` 与 `1.5.1` 完成端到端功能验证与明/暗双主题截图核验，覆盖智能体定位、子页避让恢复、供应商表单保护、真实 `models.dev` 补全与侧栏折叠 |
| 线上发布核验 | 核心 `v0.2.12` 为 GitHub 正式 Latest。GitHub 与 CNB 的固定清单均返回 HTTP 200，六个核心资产逐字节一致。客户端 `updater` 强制验签模式在 CNB 主源与 GitHub 回退源下均通过 Ed25519 签名校验；三平台 SHA-256 与归档结构检查通过，下载的 macOS ARM64 二进制报告 `0.2.12`。`plugins-latest` 独立通道双源清单保持未变且逐字节一致。 |

### 发布与后续边界

核心已通过 `v0.2.12` 注释标签触发 `.github/workflows/release.yml` 完成发布。工作流将完整资产上传至 GitHub 草稿，同步 CNB 后公开 GitHub 正式 Release。[核心发布工作流](https://github.com/wssz527/kimicode-plus/actions/runs/46010984686) · [v0.2.12 Release](https://github.com/wssz527/kimicode-plus/releases/tag/v0.2.12)

## v0.2.11 审查修复与发布交接（2026-11-27）

核心发布版本为 `0.2.11`，标签为 `v0.2.11`。官方插件使用独立的 `plugins-latest` 通道，同步更新 KCT 至 `1.0.9`（标签 `plugin-kct-v1.0.9`），Planner `1.0.8`（`minCoreVersion: 0.2.10`）、Monitor `1.1.3` 和 Scheduler `1.1.1` 保持不变。

### 交付范围

- KCT 思维链翻译彻底消除多重翻译与残留：修复卸载清理 TDZ 异常、外层折叠容器误匹配与三排工具栏叠加、流式输出 `streamSince = 0` 整段重译以及未闭合行内代码/链接提前切片外发；设置保存采用最新配置合并避免旧快照冲掉字段。
- 宿主兼容与接入稳健性：Kimi Code `1.4.3` 纳入已验证宿主版本；修复后台 `attach` 与 `attach --force` 状态回写及桥存活校验；独立版 KCT 重装 LaunchAgent 时重新触发迁移确认入口；Windows/macOS 接入锁与状态读写加严。
- 供应商模型能力与官方表单协同：仅在同一官方供应商表单成功提交后按前后快照对账恢复被官方表单丢弃的 `max_output_size` 等扩展字段，修复 KAP 跨端口/重定向凭据保护与超时语义。
- 插件运行时、日志与热更新设置面板一致性：修正 `ctx.log("info")` 误记 `ERROR`；挂起激活支持立即取消与 Scope 回收；插件资产接口端到端绑定 `version`/`updatedAt`/`capabilities` 并做读盘前后二次一致性校验，设置面板挂载与取消信号竞速、区分停用/宿主卸载/激活失败错误码，并在忙碌期排队刷新及官方更新短暂 `Disable -> Enable` 后自动恢复重挂。
- 同步修正 `README.md`、`PRODUCT.md`、`CacheSettings.tsx`、`FRONTEND.md`、`PLUGINS.md`、`API_CONTRACT.md`、`NATIVE_INTEGRATION.md` 中滞后的保活与插件契约说明并清理死代码。
- 隔离验收启动核心时默认设置 `EKD_DISABLE_OS_KEYCHAIN=1` 和 `EKD_DISABLE_AUTO_UPDATE_CHECK=1`，禁用系统钥匙串与自动更新检查。M5 验收同时使用临时 `HOME`。

### 验证与复审

| 项目 | 结果 |
|---|---|
| 前端 | TypeScript 类型检查、424/424 单测、普通与宿主生产构建通过 |
| Go | `go vet ./cmd/... ./internal/... ./plugins/...` 与 `./cmd/... ./internal/... ./plugins/...` 全量测试通过 |
| 打包与同步 | `scripts/test_package.py` 11/11、`scripts/test_sync_cnb_release.py` 7/7 通过 |
| 真实二进制 | `scripts/acceptance.py --binary ./dist/enhanced-kimi` 27/27、`scripts/acceptance_m5.py ./dist/enhanced-kimi` 10/10 通过 |
| 独立复审 | 业务改动与验收隔离脚本均经独立复审，发现项已修复闭合 |
| 线上发布核验 | 核心 `v0.2.11` 为 GitHub 正式 Latest。GitHub 与 CNB 的固定清单均返回 HTTP 200，六个核心资产逐字节一致。清单 Ed25519 签名、三平台 SHA-256 和归档结构检查通过，下载的 macOS ARM64 二进制报告 `0.2.11`。`plugins-latest` 保持独立通道，双源清单与四个插件 ZIP 逐字节一致，签名、ZIP SHA-256 和目录内容摘要均通过内置公钥与发布工具校验。 |

### 发布与后续边界

核心已通过 `v0.2.11` 注释标签触发 `.github/workflows/release.yml` 完成发布。工作流将完整资产上传至 GitHub 草稿，同步 CNB 后公开 GitHub 正式 Release。官方插件已通过 `plugin-kct-v1.0.9` 标签触发 `.github/workflows/release-plugins.yml`，完成打包、签名和反向校验，并更新 GitHub 与 CNB 的 `plugins-latest`。两端均在归档上传完成后发布清单。[核心发布工作流](https://github.com/wssz527/kimicode-plus/actions/runs/45878157458) · [插件发布工作流](https://github.com/wssz527/kimicode-plus/actions/runs/45878152610)

本轮未调用真实付费翻译服务、真实账号 KAP 流程和系统文件选择窗口，完整桌面客户端宿主也未进行实机操作。保活核对之后才落盘的步骤不回补记账。自定义中继仅在符合官方主机名单时发送私有 `thinking`。真实线上插件热替换、同账号有无保活的长周期缓存 TTL 对照以及八站登录态提取与回填仍待进一步实测。

## v0.2.10 审查修复与发布交接（2026-11-26）

核心发布版本为 `0.2.10`，标签为 `v0.2.10`。官方插件使用独立的 `plugins-latest` 通道，包含 KCT `1.0.8`、Planner `1.0.8`、Monitor `1.1.3` 和 Scheduler `1.1.1`；Planner 的最低核心版本为 `0.2.10`。

### 交付范围

- 同屏会话继续对齐原生聊天交互；规划器补充思考展示、线程恢复与原生交接；模型能力配置、缓存保活和预算控制沿用既有核心与官方设置入口。
- 插件、桥接、网关和更新边界完成安全加固。规划请求只向 Kimi/Moonshot 官方主机发送私有 `thinking`，其他 OpenAI 兼容服务不在未配置输出上限时盲目发送 `max_tokens`；Responses、Anthropic 和 Gemini 的截断统一映射为 `length`。
- KCT 合并相同配置与原文的在途请求，任务固定设置快照，折叠、取消、重试、流式收尾和原文替换不再造成重复请求、串服务或状态残留。同屏与规划器仅在用户点击后翻译，使用原始 Markdown 保护代码块，支持原文/译文切换、设置浮层和停用清理。
- 定时任务优先按官方 `user_message_id` 归属回合；忙碌后回到空闲仍无归属证据时明确失败，不误报完成。保活按已匹配回合累计核对时已落盘的主代理步骤用量，首个步骤决定缓存命中，发送前估算继续作为记账下限。
- 插件附件选择通过渲染层文件控件进入既有暂存通道，取消不会暂存旧文件；核心按暂存路径校验和清理批次。接口说明见 `PLUGINS.md`。

### 验证与复审

| 项目 | 结果 |
|---|---|
| 前端 | TypeScript 类型检查、368/368 测试、普通与宿主生产构建通过 |
| Go | `go vet ./cmd/... ./internal/... ./plugins/...` 与生产范围全量测试通过；调度和保活聚焦竞态测试通过 |
| 打包与同步 | `scripts/test_package.py` 11/11、`scripts/test_sync_cnb_release.py` 7/7 通过 |
| 真实二进制 | `scripts/acceptance.py --binary ./dist/enhanced-kimi` 27/27 通过 |
| 真实浏览器隔离页 | 同屏与规划器展开时不发翻译请求，两次手动点击共两次请求；Markdown 代码保留、原文切换、设置二次关闭、主题切换与停用清理通过 |
| 独立复审 | DeepSeek 分别复核后端与前端；后端无新增代码发现，前端四项发现均已修复并闭合复核 |
| 线上发布核验 | 核心 `v0.2.10` 为 GitHub 正式 Latest，六个资产完整；清单 Ed25519 签名、三平台 SHA-256、归档结构和 CNB 字节一致性通过，下载的 macOS ARM64 二进制实际报告 `0.2.10`。四个插件经内置公钥反向验签与内容摘要校验，GitHub/CNB 清单及安装包一致。 |

### 发布与后续边界

核心已通过 `v0.2.10` 注释标签完成 `.github/workflows/release.yml` 发布，完整 GitHub 草稿同步 CNB 后公开为正式 Release。插件已由 `plugin-kct-v1.0.8` 标签完成 `.github/workflows/release-plugins.yml` 发布，打包、签名和反向校验后更新 GitHub 与 CNB 的 `plugins-latest`；清单在归档之后发布。线上核验记录见上表。

真实付费翻译服务、真实账号 KAP 流程和系统文件选择窗口未调用；完整桌面客户端宿主未在本轮实测，内置浏览器的 Shadow DOM 验收不能替代该项。保活核对之后才落盘的步骤不回补记账。自定义中继即使转发到 Kimi，也只有符合官方主机名单时才发送私有 `thinking`。真实线上插件热替换、长周期缓存 TTL 对照和八站登录态提取/回填仍按既有边界继续观察。

## v0.2.4–v0.2.8 关键演进与验证边界

| 版本 | 主要变化 | 已验证 | 仍未闭环 |
|---|---|---|---|
| v0.2.4 | 自更新改为 CNB 国内镜像优先、GitHub 兜底的双源 `manifest.json` 入口，发版时自动同步 CNB | 清单读取、源回退、`SHA256SUMS` 校验与自替换路径；同步脚本与打包测试见 `scripts/test_sync_cnb_release.py`、`scripts/test_package.py` | 国内网络的真实限速与断网切换只在本地合成源上验证 |
| v0.2.5 | 会话扫描跟随符号链接并按规范路径去重；Monitor localStorage 垫片只拦截自有键，历史回放不再重复累加；Monitor 1.1.1、KCT 1.0.4 | 缓存统计与 Monitor 日用量回归、旧版升级时插件文件同步落盘 | 外置盘长期挂载/卸载抖动下的统计仍靠单次脚本复现 |
| v0.2.6 | 修复后台子代理计数永久卡死与账号切换忙碌误判；切号改为风险提示 + 「等完成后切换」/「仍然立即切换」（显式 `force:true` 保留备份与回滚） | `findBusySessions` 以官方实时 `activity` 为准的回归、排队与强制切号路径 | 真实多账号长周期并发下的排队时序未做连续观测 |
| v0.2.7 | 修复自更新后 Kimi Code 渲染进程卡死：注入改为全局重入串行队列 + 代际淘汰，卸载单飞幂等，跨进程 `host-attach.lock` 串行化接入 | 双端升级后重连、旧实例回收、9229 端口冲突消除的实机复现与回归 | 频繁连续升级的压力场景未长期跑测 |
| v0.2.8 | KCT 译文恢复官方 `.think-text` 浅灰样式；新增官方插件独立在线热更新（`plugins-latest` + Ed25519 验签 + 扩权二次确认） | 签名校验、`minCoreVersion` 拒绝、能力差异确认、重启后仍按内置公钥复核；插件列表/详情与实验室页的检查与一键更新 | 真实线上插件新版热替换的长期观察；真实账号长周期缓存 TTL 对比 |

三条诚实边界保留如下，不因发布而改写：**真实线上插件新版热替换**目前只完成发布通道与本地签名流程验证，尚未经历一次真实的线上插件版本迭代并观察客户端热替换；**真实账号长周期缓存 TTL 对比**仍缺同账号有/无保活的跨小时对照，现有 `hit` 回执证明该探测回合读取了缓存，真实缓存寿命需要有无保活对照；**8 个站点（ChatGPT、Claude、Gemini、DeepSeek、Perplexity、豆包、通义千问、元宝）的登录态提取与回填**仍待实机 E2E 闭环，现有证据是合成 DOM 单测与本地合成网页的选区提取。

## v0.2.3 会话审查修复与发布验收

| 场景 | 结果 |
|---|---|
| 更新通道与自动检查 | 默认更新检查改用 `releases/latest/download/manifest.json`，不再依赖 GitHub 匿名 REST API；安装包与 `SHA256SUMS` 仍来自同一 Release，保留完整校验和重启交接。真实 GitHub 调用从 0.2.1 正确发现 0.2.2。成功更新后的持久化 `updated` 回执不再永久阻断后续自动检查。 |
| 更新回滚与恢复 | 覆盖旧核心恢复健康、旧核心仍不健康、launchd 任务移除但进程异步退出、缺失数据库备份和活动事务清理失败等路径。数据库与二进制一旦完成还原，后续启动不得再次套用旧快照；launchd 文件替换前同时确认任务与真实 PID 已退出。 |
| 插件与迁移安全 | `net:https` 拒绝非规范 IP、带 zone 的 IPv6 和单标签内网主机；代理 DNS 失败不再成为绕过入口。Monitor 在当前 `index.html` 已干净时只清理残留备份，不覆盖当前页面；备份与当前注入页不一致时从当前页面移除加载器。 |
| 账号、续期与设置 | 错峰并发 401 复用已刷新会话；跨 OAuth Key 切号同步目标 `baseUrl` 并清除旧主机字段；桥接只重试派发前拒绝；`activity:null` 回退旧版空闲字段；两条凭据重置路径都清除账号密文、元数据和待处理切换；KCT 设置进入官方插件页的 KimiCode+ 分段。 |
| 全量回归 | `make test`、`make check`、全仓生产范围 `go test -race -count=1` 和 Windows amd64 `go vet` 全部通过；前端 TypeScript 与 321/321 测试通过；普通及 host 构建完成；真实二进制验收 27/27；打包测试 10/10。最终 macOS ARM64 二进制报告 0.2.3。 |

## v0.2.1 稳定性修复与双端实机验收

| 场景 | 结果 |
|---|---|
| 全仓单测与并发检查 | `go vet`（macOS + Windows）零告警；全仓 23/23 包 `go test -race -count=1` 全部通过（含进程级更新与 SQLite 数据库回滚矩阵）；前端 TypeScript 类型检查与 308/308 单测通过；`scripts/test_package.py` 9/9 通过；真实二进制 `scripts/acceptance.py` 27/27 通过。 |
| macOS ARM64 实机隔离验收（官方 1.4.2） | 归档 `enhanced-kimi-0.2.1-darwin-arm64.tar.gz` 摘要校验与解压完整，`--version` 输出 `0.2.1`。实测 `401 TOKEN_EXPIRED` 与 `403 PERMISSION_DENIED` 拆分及 `update:read/write` 权限边界；原生 `main-loopback-rpc` 保原 PID 附着 Kimi Code 1.4.2 成功，临时 9229 调试端口确认关闭（修复 `CDP.Close` 在 `StatusNoStatusRcvd` 时的误报），`attach-state.json` 写入 `corePid`；1.4.2 `sessions.list`/`session.create`/`session.watch`/`session.unwatch` 通过；停止旧核心后同端口以 `--resident` 启动新核心（不重新附着），既有 `bootstrap.js` 自动读取 `runtime.json` 完成续期并恢复长轮询连接。日常实例零干扰。 |
| macOS ARM64 真实 `launchd` 登录项与自更新实机验收 | 实测 `attach --install` 首次同步 `LICENSE` 与 `THIRD_PARTY_NOTICES.txt`；删除模拟下载目录后经真实 `launchctl kickstart -k` 正常启动且诊断页可读常驻许可；跨数据目录 `attach --uninstall` 被拦截且不干扰运行中核心。真实 `launchd`（`KeepAlive=true`）下完成 `0.2.0 → 0.2.1` 健康单进程交接（修复 `launchdLabelFor` 误排除常驻自身与 `AcquireCore` 交接误回滚），以及候选破坏 SQLite `schema_versions` 后的 `bootout` 止损、数据库与二进制原子回滚及 `bootstrap/kickstart` 恢复（`plugins` 模块 `READY`，单进程，事务与备份自动清理）。 |
| Windows x64 实机隔离验收（`wssz-win`，官方 1.4.2） | 归档 `enhanced-kimi-0.2.1-windows-amd64.zip` 摘要校验与解压完整，`--version` 输出 `0.2.1`。实测 401/403 与 `update:read/write` 边界、`diagnostics.get` 许可读取、Session 1 原生 `main-loopback-rpc` 保原 PID 附着、9229 端口确认关闭、`corePid` 落盘、1.4.2 `session.create`/`session.watch`/`session.unwatch` 及 Core 重启免重接自动续期全部通过。日常核心与日常客户端全程零干扰，远端临时目录已清理。 |

## v0.2.0 同屏会话与发布回归

| 场景 | 结果 |
|---|---|
| macOS ARM64，官方 1.4.2 | 隔离宿主通过 `main-loopback-rpc` 接入。修复前，官方 `/api/v1/sessions` 返回旧会话和字符串 `activity`，增强列表返回 0 条，新建与详情误报缺少 ID。修复后可打开旧会话及历史，在主窗格与分栏分别新建会话，分栏实时显示发送和本机合成回复。由官方界面新建并发出首条消息后，同屏选择器可立即找到会话，并加载其对话。 |
| Windows x64，官方 1.4.2 | 隔离宿主通过 `windows-cdp-loopback` 接入。对照官方 KAP 和核心列表，两者返回相同的会话 ID。模拟第一次列表响应暂时为空后，保持同一选择器即可加载旧会话。分栏新建后可发送消息并实时看到本机合成回复。 |
| Linux 拒绝边界 | Linux amd64 `go vet ./...` 和桌面测试二进制交叉编译通过。Linux ARM64 隔离虚拟机运行四项回归通过，覆盖未设置安全模式回调、拒绝不受支持的宿主接入、状态边界及会话读取和新建。 |
| 构建与发布归档 | Web 类型检查与 297 项测试、`make build`、二进制验收 27 项通过。`dist/release/` 下三个平台包逐包校验二进制、`LICENSE`、`THIRD_PARTY_NOTICES.txt`、`RUN.txt` 和 SHA-256。 |

## v0.2.0 登录任务与常驻路径补验

| 场景 | 结果 |
|---|---|
| Windows x64 真实交互桌面 | 在 `wssz-win` 的控制台会话（2560×1440）触发产品登录任务并截取整屏桌面对比。旧版（`2e1c6eb` 直接任务）弹出可见的黑色 Windows Terminal 窗口（`consoleVisible: true`，`terminalHasCore: 1`）。新版将可执行文件和数据目录放在含空格与中文的路径（`测试 目录\enhanced-kimi.exe`、`数据 目录`），经 `wscript.exe` 无窗口入口启动后桌面无弹窗、无对应 Terminal 标签（`consoleVisible: false`，`terminalHasCore: 0`），核心健康检查通过，普通启动的隔离官方 1.3.2 自动完成保原 PID 接入（`transport: "main-loopback-rpc"`）。直接调用 `launch` 和 `attach --status` 均输出预期 JSON，关闭自动接入时一并移除登录任务、标记文件与 `windows-resident.vbs`。整屏对比截图与探针记录保存在 `work/release-followup/`（`windows-before-desktop.png`、`windows-after-desktop.png`、`verify-summary.json`）。 |
| Windows 空闲后台 CPU | 两版在同一台 16 逻辑处理器机器上各采样约 64 秒，隔离 Kimi Code 未启动。旧版观察到 11 个 PowerShell 子进程，累计至少 3531 毫秒 CPU 时间，占整机 CPU 容量至少 0.344%。新版观察到 0 个此类子进程，核心 CPU 时间增量低于本次采样分辨率。该方法不计入共享 WMI 服务开销。 |
| Windows 接入失败与重开 | 占用临时 inspector 端口，官方 1.3.2 的无调试参数隔离实例报告接入失败。释放端口后，重开动作在会话空闲时正常退出旧进程，新进程完成主进程接入。内置浏览器用已记录的状态验证独立设置页提示、会话运行时拒绝文案和刷新后的接入状态。Go 回归覆盖四位 fuse v1、末尾追加位和缺失或关闭所需位。 |
| macOS 常驻路径与隔离属性 | 本机测试二进制可由 launchd 从「下载」直接运行。安装隔离常驻副本后移走原件，真实 launchd 仍能从增强数据目录启动核心；停用后台接入后使用新版本重新开启会自动刷新常驻副本，登录项运行期间则拒绝静默覆盖并拒绝符号链接目标。本机实测 `com.apple.quarantine`：未批准的 `0083` 被 Gatekeeper 拦截；带系统设置「仍要打开」批准位 `00c3` 或 `xattr -dr` 清除后，`/bin/cp -p` 复制的副本即使删去「下载」原件，真实 `launchctl bootstrap` 仍以退出码 0 正常启动（记录在 `work/release-followup/mac-quarantine-verify.json`）。进程级自更新测试通过模拟发布源完成健康切换，常驻副本更新，下载原件未改动。 |
| 发布平台与 CI | v0.2.0 发布归档为 `darwin-arm64`、`darwin-amd64` 和 `windows-amd64`。Linux x64 继续运行 Ubuntu CI 和交叉检查，覆盖不支持原生接入的平台拒绝路径。 |
| Windows x64 重启后发布包复验 | `wssz-win` 于 2026-11-20 19:18 重启并登录控制台会话 1 后，将最终打包的 `enhanced-kimi-0.2.0-windows-amd64.zip` 与 `SHA256SUMS` 传至隔离目录实测：压缩包 SHA-256 校验通过，解压包含预期四项文件，`--version` 为 `0.2.0`；在中文空格数据目录注册登录任务并触发后，核心在会话 1 无窗口运行（`coreMainWindowHandle: 0`）、健康接口返回 `OK` / `0.2.0`，普通启动的隔离官方 1.3.2 自动完成保原 PID 接入（`hostPid: 8996`，`transport: "main-loopback-rpc"`）；卸载后登录任务、标记文件与 `windows-resident.vbs` 全部清空，临时目录已删除（记录见 `work/release-followup/windows-postboot-verify.json`）。 |
| Windows x64 整机重启登录自动触发实测 | 将最终打包的 `enhanced-kimi-0.2.0-windows-amd64.zip` 预装至 `wssz-win` 隔离目录（含中文与空格数据目录 `数据 目录`）并注册登录任务 `KimiCodePlus-6104a0f6782ac9f0`（`MSFT_TaskLogonTrigger`、`Interactive`）后，用户于 2026-11-20 20:25:05 整机重启并在 20:26 登录控制台会话 1。Windows 任务计划程序于 20:26:22 自动触发该任务，常驻核心以 PID `10284` 在会话 1 无窗口运行（`coreMainWindowHandle: 0`），健康接口返回 `OK` / `0.2.0`；随后普通启动无调试参数的隔离官方 1.3.2（PID `8824`），自动拉起的核心直接完成保原 PID 主进程接入（`transport: "main-loopback-rpc"`）。卸载后登录任务、`windows-resident.vbs`、标记文件与隔离目录已全部清空（记录见 `work/release-followup/windows-logon-e2e.json`）。 |

Windows 整机重启登录自动触发、无窗口常驻与自动接入已全链路实测通过。macOS 隔离属性使用本机构建二进制写入标准 `com.apple.quarantine` 标志实测，未走浏览器真实点击下载与系统设置界面点击。发布目标为 macOS Apple Silicon、macOS Intel 和 Windows x64。

## v0.2.0 自动接入与登录常驻复验

| 场景 | 结果 |
|---|---|
| Windows x64 / 官方 1.3.2 | `wssz-win` 的交互桌面开启自动接入后，未带调试参数的隔离客户端保持 PID 6572，接入成功且临时 inspector 关闭。手动触发产品登录任务后，隔离核心启动并完成接入。按普通方式重开客户端后，新 PID 19332 自动接入。 |
| Windows 兼容模式重开 | 隔离 Edge 的真实插件 ZIP 预览显示三条中文限制和「立即重新打开」。点击后，隔离官方客户端从 PID 3620 切换到 14676，新进程接入成功。用本机合成模型保持真实官方客户端的回合活动时，重开返回 `409 HOST_SESSIONS_BUSY`，前后均确认回合仍在运行。 |
| macOS ARM64 / 官方 1.3.7 | 在真实 launchd 中加载产品生成的隔离标签。常驻核心接入隔离客户端，客户端重开后接入新 PID。终止隔离常驻核心后，launchd 启动新核心并接入原客户端。自动接入兼容包含中文的 profile 路径。实测连续两次客户端心跳唤醒，接入时间保持原值。 |
| Go、Web 与构建验收 | 所有生产 Go 包的测试与 vet 通过。TypeScript 检查通过，Web 296 项测试通过。`make build` 与 `scripts/acceptance.py` 通过，核心验收共 27 项。 |

Windows 探针确认官方 Electron 45.1.0 的 inspector 与 RunAsNode fuse 已启用。SSH 会话与交互桌面隔离，探针进入同一桌面会话后才成功开启目标 inspector。双平台记录位于 `work/auto-attach/`。系统注销再登录、整机冷启动、Windows ARM64 真机和日常配置迁移仍未实测。

## v0.2.0 发布前复验

F1 切号回滚测试覆盖凭据恢复失败、配置恢复失败、两项均失败、两项均成功，以及成功切换但备份清理失败。保活测试确认两次 Git 状态文本相同且文件内容不同时会跳过撤回，读取无法确认时也会跳过。

F2 使用 macOS 隔离 Kimi Code 1.3.7 验证安装预览、高权限二次确认、包内容变化后的 `409 PLUGIN_PACKAGE_CHANGED` 以及重新预览时清空授权。回归测试覆盖两个插件先后挂载分栏及两种关闭顺序。Windows amd64 候选在 `wssz-win` 上实际完成 v2 ZIP 预览和安装、摘要失配拒绝、命令注册及核心调用路由。使用同一台 Windows 主机上的独立无头 Edge 打开候选内置工作台，确认真实预览数据在深、浅色下均显示三条「Windows 暂不可用」说明。

下表记录 Windows 渲染进程兼容模式的发布前验收。新增的主进程桥验收见本节后面的记录。

| Windows 能力 | 兼容模式与复验范围 |
|---|---|
| 本地 ZIP / 文件夹安装 | 官方 1.2.1 实测系统 ZIP 对话框回填、预览及授权安装。官方 1.3.2 实测系统目录对话框回填、预览及授权安装。直接调用 Windows 核心桥也验证了两种后备对话框，均返回预期路径。 |
| 插件文件和目录选择、命令 | 官方 1.3.2 中通过插件真实面板选择文件和目录并校验返回路径，实际命令回调返回测试标记。核心另有注册、鉴权与路由回归。 |
| 主区整页和分栏、右侧标签、会话信息入口、右侧面板列表 | 官方 1.3.2 实测左侧入口、整页开关、新建会话释放插件整页，以及两个独立插件按两种顺序替换分栏。会话信息及右侧面板列表均可打开实际插件标签。 |
| 同文档「+」菜单 | 由 DOM 回归覆盖。Windows 1.3.2 的实际「+」入口使用独立浮层，同文档变体待具备该界面的 Windows 宿主复验。 |
| 插件独立网页、独立弹出窗、独立的右侧「+」浮层 | Windows 暂不可用。真实安装预览显示三项限制。官方 1.3.2 中插件发起网页和窗口调用，均返回 `PLATFORM_UNSUPPORTED` 与中文说明。直接调用 `wssz-win` 核心桥的独立浮层同步动作也返回相同错误码。 |

Windows 主进程桥随后在 `wssz-win` 的官方 Kimi Code 1.3.2 / Electron 45.1.0 上补验。第三方 v2 插件通过官方独立「+」浮层打开右侧标签，创建原生网页视图并完成正文提取、导航、适配器和关闭，随后打开独立插件窗口并调用其中的命令。测试使用本机合成模型和网页。恢复已有会话可正常接入，主进程临时调试端口确认关闭。增强核心在原端口重启并更换配对令牌后，不重启官方宿主即可再次通过三项检查。对应记录在 `work/windows-plugin-surfaces/`。

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

Windows 缓存扫描使用文件句柄的卷序列号和文件索引区分追加、截断及替换。`wssz-win` 已通过对应回归，最终归档再次通过第三方 ZIP 安装、命令路由、正常更新及故障回滚。Windows 测试使用真实路径的 JSON 编码，按平台验证启动器和文件权限，并等待 Node 探针关闭后自然退出。已登录的 `wssz-win` 控制台另通过官方 1.2.1 和 1.3.2 的隔离宿主接入。Chromium 拒绝带浏览器 `Origin` 的 CDP 握手，修复后的本地客户端直接连接已验证的端口。空会话可启用带右侧面板的插件，进入测试会话后可从官方入口打开。会话使用在 Windows 回环地址运行的本地模型夹具。证据见 `work/release-fix-20261119/windows-followup/`。

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
| N4 空闲会话保活（历史） | 隔离 1.3.7 与合成上游的前缀比对及深浅色截图见 `work/next-round-20261119/n4-acceptance-report.json`。当前统一空闲保活策略和回归证据见下方。 |
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
| 保活策略 | 总开关优先、历史冷会话等待真实请求、后台等待期间没有保活提示或压缩请求 | `TestKeepAliveGlobalSwitchAndColdActivation`、`TestAutomationSkipsLegacySteerAndProtectsBackgroundWait` |
| 保活回执及护栏 | 分桶观测驱动间隔；同回合用量判定发送、命中、未命中；三类供应商预算、未命中熔断、退避和慢回合 | `TestKeepAliveRetentionPolicy`、`TestJudgeKeepAliveVerdicts`、`TestKeepAliveBudgetAndMissFuseAcrossProviders`、`TestKeepAliveSlowTurnAndTransientRejection` |
| 设置与撤回 | 查询不写历史设置或初始化缓存表；旧截止模式显示停用原因；工作区变化和新活动时跳过撤回 | `TestKeepAliveReadDoesNotWriteAndLegacyModeStops`、`TestIdleKeepAliveUndoSafetyChecks`、`TestIdleKeepAliveRechecksActivityAfterGitScan` |
| 真实会话手算 | 3 个会话与手算一致（含 931657/0.990900 的重合） | 见下方实测记录 |
| 前端 | typecheck 通过、81 项测试全绿、build 成功 | `web/` |
| 真实二进制端到端 | 隔离宿主上 `cache.summary`/`sessions`/`history`/`retention`/`receipts` 返回真实值；`monitor.summary` 已删除，调用返回未知动作；历史设置写入可读回 | 隔离宿主运行日志、`TestMonitorSummaryActionIsGone` |

| 页面截图 | 深浅两种配色均渲染正确：四张指标卡并排、会话表格三列、策略开关、留存条、实际用量柱状图、最近执行记录三种回执状态 | `work/ab/shots/cache-light.png`、`cache-dark.png` |

### 历史后台插话实测（隔离宿主，合成上游）

下列记录用于追溯已移除的后台插话路径。当前保活只在空闲时发出独立请求，并使用精确回合归属。

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

### 历史后台等待 A/B 实测（D8 发现阶段）

环境：`../work/ekd-native-acceptance` 隔离宿主（官方 2.6.0，独立 `KIMI_CODE_HOME` 与独立 Electron profile），合成 OpenAI 兼容上游记录每次请求并上报 `cached_tokens`。provider `base_url` 经官方 `/config` 热改并在结束后还原。未接管日常实例、未安装真实 LaunchAgent、未调用付费模型。

- **旧版状态 A（后台等待）可发送插话**：首条消息产生请求 → 回合结束 `busy=false` → 保活消息 HTTP 200 → 上游记录到携带保活标记的新请求（4 次请求中 2 次命中标记）。
- **旧版状态 B（阻塞等待）未产生新请求**：合成上游返回真实 `Bash` 工具调用执行 `sleep 75`，阻塞窗口内发送保活消息被接收，但阻塞期间与工具结束后均**没有**新增请求，保活标记未出现在任何请求里。旧版实现因此记 `skipped`。

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
