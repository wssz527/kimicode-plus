# KimiCode+ (Enhanced Kimi Desktop)

独立的 Kimi Code Desktop 模块化增强套件（非官方，不修改官方程序安装包）。Go 承担业务、受保护的本机 Loopback HTTP、加密凭据/账号库与纯 Go SQLite，React 静态界面通过 `go:embed` 内嵌发布为单一可执行文件；原生模式直接复用官方客户端已有的 Electron/Chromium，无需额外安装 Node.js 或独立浏览器运行时。

## 核心能力

- **跨版本契约注入（macOS & Windows）**：不再按官方版本号白名单拦截，改为基于进程签名、Electron 调试熔丝与 15 项 DOM/API 运行时契约探针按功能点放行。已人工验证的版本显示已验证状态，其他版本（如旧版 1.3.2 或未来更新版本）在安全门槛与探针通过时照常运行并给出提示；若官方后续改版影响单个注入点，仅停用或回落该功能，不影响其余能力。
  - **macOS**：通过主进程临时调试端点注入与可开关的 `LaunchAgent` 后台自动接入。
  - **Windows**：通过固定路径启动器（`enhanced-kimi launch`）、本机环回 CDP 注入（带外部调试连接安全审计）及可一键备份/恢复的开始菜单与桌面快捷方式实现原生接入。
- **多账号一键切换（不重启、不断会话）**：在官方「账户」设置页顶部与左下角账号菜单中直接保存与切换多个 Kimi Code 订阅账号。凭证经 PBKDF2-SHA256 + AES-256-GCM 加密保存在独立账号库中，切换时先备份再原子写入官方凭证文件，失败自动回滚，忙态会话支持排队等待完成后切换。
- **缓存观测与空闲保活**：查看本机缓存命中、会话空闲时长与实际用量；支持后台等待保活、可选的空闲会话保活（按供应商缓存 TTL 自动推导间隔，发送极短提示并经官方 `:undo` 撤回该轮、保活期间禁压缩，支持单会话在「会话信息」单行菜单覆盖）、空闲压缩与额度恢复。
- **Kimi Code 原生配置增强与设置归位**：所有增强设置归位到官方对应标签页，不再维护平行配置页——
  - 官方「模型供应商」页：每个模型行提供「能力与限制」内联抽屉，可视化配置图片输入、视频输入、工具调用、思考强度（`no / low / medium / high / xhigh / max`）、上下文窗口与最大输出长度。
  - 官方「智能体与会话」页：配置权限规则与危险命令护栏、子代理模型池、子代理时限与单轮步数上限。
  - 官方「实验室」页：提供实验性「会话并排」开关。
  - 官方设置内新增「KimiCode+」标签页：平铺展示「缓存」「增强插件」「状态与诊断」三节；若官方页探针失效，对应内容自动回落至本页。
- **预装增强插件（均默认关闭，按需启停）**：与官方插件市场相互独立，具备独立能力门禁、故障隔离与许可证查看入口——
  - **定时任务（Scheduler）**：单次/每日多时刻/每周/间隔调度、错过补跑或跳过、提示词变量插入与预览、标题 `[定时] ` 前缀、已读后走官方 `:archive` 归档、仅补缺官方未覆盖的失败/跳过系统通知（跟随官方系统通知与提示音开关）、单任务与全局成本护栏、运行前校验及 `yolo` 脏仓库二次确认。
  - **Monitor**：基于开源项目 [bfjnbvf/kimi-code-monitor](https://github.com/bfjnbvf/kimi-code-monitor)（MIT）适配的侧栏模块化面板与 Rive 蓝球形象，合并展示 Kimi Code 5h/每周额度、加油包及官方已配置外部供应商余额（DeepSeek、Moonshot、BigModel、Magpie 等），读取失败绝不伪造为 0。
  - **规划（Planner）**：本地持久化规划线程与网页规划交接，支持多选消息、编辑预览后发送到新建或已有 Code 会话。
  - **思考内容翻译（KCT）**：适配自独立版 [wssz527/kimi-cot-translate](https://github.com/wssz527/kimi-cot-translate)（MIT），流式翻译并展示模型思考块内容。
- **第三方增强插件**。`apiVersion: 2` 支持左侧栏导航、主聊天区整页或分栏、右侧面板、会话入口和网页视图等挂载点，也提供会话、命令、附件、规划和定时任务接口。平台限制以安装预览和[增强插件开发文档](docs/PLUGINS.md)为准。从本地文件夹或 ZIP 安装时可逐项授权，高权限能力需要二次确认。插件包发生变化后须重新预览。
- **GitHub Releases 自更新**。在「KimiCode+ → 状态与诊断」检查新版、查看更新说明并下载与版本和平台匹配的安装包。核心校验 SHA-256，并在新版通过健康检查后确认成功。启动失败会尝试恢复旧核心，诊断页会显示结果。

下载与版本说明见 [GitHub Releases](https://github.com/wssz527/kimicode-plus/releases)。验证记录与已知边界见 [docs/VALIDATION.md](docs/VALIDATION.md)。

## 第三方增强插件：安装与开发

### 如何安装第三方插件

1. 打开 Kimi Code 设置 → **KimiCode+ → 增强插件** → 点击 **安装本地增强插件**。
2. 点击 **选择文件夹** 或 **选择 .zip 包**（也可直接粘贴本地路径），点击 **检查增强插件包**。
3. 核对插件信息、上游来源（如有）与申请的能力列表，按需勾选授权；若包含高权限能力，需勾选风险确认框后完成安装。
4. 新安装的插件默认处于停用状态，确认无误后点击 **启用增强插件** 即可生效，详情页可随时查看该插件自带的 `LICENSE`、`NOTICE` 与活动记录。

### 最小插件结构与开放挂载点

```text
my-plugin/
  plugin.json      # 必填：id（发布者.名称）、version、apiVersion（1 或 2）、hostVersions、entry、capabilities
  renderer.js      # 必填：ES module，导出 activate(ctx)
  settings.js      # 可选：ES module，导出 renderSettings(ctx, element)
  assets/          # 可选：插件私有静态资源
  LICENSE          # 必填：开源或使用许可文件
  NOTICE           # 可选：上游来源与版权说明（详情页可直接展开查看）
```

Windows 支持本地 ZIP 和文件夹安装、插件命令、右侧标签和会话信息入口。通过 KimiCode+ 启动器打开官方客户端并成功接入 Electron 主进程后，插件还可使用独立网页视图、插件弹出窗和官方右侧「+」浮层。仅接入渲染进程时，安装预览会列出受限能力。

可复制 [plugins/example-hello/](plugins/example-hello/) 开始开发。能力清单和挂载示例见 [增强插件开发文档](docs/PLUGINS.md)。

## 快速开始

### 构建单文件二进制

构建需要 Go 1.26+、Node.js 24+、npm 与 Python 3。SQLite 构建使用纯 Go，设置 `CGO_ENABLED=0`。

```bash
sh scripts/build.sh        # 构建前端并生成本机 dist/enhanced-kimi
sh scripts/crossbuild.sh   # 交叉编译 macOS / Windows / Linux 产物
```

### macOS 接入官方客户端

```bash
# 1. 启动增强核心（接入日常客户端需显式 --allow-daily-instance）
./dist/enhanced-kimi --allow-daily-instance

# 2. 使用 runtime.json 中的 url 与 pairingToken 执行单次接入
./dist/enhanced-kimi attach \
  --data-dir "$HOME/Library/Application Support/EnhancedKimiDesktop" \
  --kimi-home "$HOME/.kimi-code" \
  --allow-daily-instance \
  --core-url "<runtime.url>" \
  --pairing-token "<runtime.pairingToken>"
```

可在 KimiCode+「状态与诊断」中一键开启或关闭「随 Kimi Code 启动自动接入」（`LaunchAgent`）。

### Windows 接入官方客户端

```powershell
# 启动或复用增强核心并自动拉起/接入官方 Kimi Code
.\dist\enhanced-kimi-windows-amd64.exe launch --allow-daily-instance
```

可在 KimiCode+「状态与诊断」中一键开启「随 Kimi Code 启动自动接入」（自动备份开始菜单和桌面快捷方式，关闭即无损恢复原快捷方式）。

### 隔离测试与验证命令

接入隔离官方宿主时请始终使用独立 `--data-dir`、`--host-profile` 与 `--kimi-home`，避免触碰日常配置：

```bash
./dist/enhanced-kimi \
  --data-dir /absolute/path/to/ekd-data \
  --host-profile /absolute/path/to/isolated-profile \
  --kimi-home /absolute/path/to/isolated-kimi-home
```

标准测试与构建命令：

```bash
go test ./cmd/... ./internal/... ./plugins/... -count=1
go vet ./cmd/... ./internal/... ./plugins/...
(cd web && npm run typecheck && npm test && npm run build && npm run build:host)
python3 scripts/acceptance.py --binary ./dist/enhanced-kimi
```

开发模式：

```bash
go run ./cmd/enhanced-kimi --data-dir ./work/development --port 43125
(cd web && npm run dev)
```

## 目录

| 路径 | 责任 |
|---|---|
| `cmd/enhanced-kimi` | 启动、数据目录锁、信号退出、运行凭据 |
| `internal/server`、`security`、`config` | 模块接线、访问控制、安全模式、设置 |
| `internal/storage` | SQLite、迁移、事务 |
| `internal/registry`、`usage` | 模型、版本变更、公共目录、独立用量账本 |
| `internal/provider`、`chat` | Go 供应商协议、账号与显式只读导入、独立聊天与请求状态 |
| `internal/gateway` | 本机令牌、模型路由、OpenAI/Kimi 流式与工具回环、网关用量归因 |
| `internal/credentials` | 口令解锁的加密凭据库 |
| `internal/bridge` | 旧版任务、附件、Outbox 与 Mock transport |
| `internal/desktop` | KAP 会话/事件、供应商模型能力读写、规划接入、原生运行时 RPC、自动化调度与回执、余额聚合服务 |
| `internal/quota` | Kimi Code 订阅与加油包额度、DeepSeek / Moonshot / BigModel / 本机 Magpie 余额读取与身份级回退 |
| `internal/scheduler` | 持久化定时任务调度引擎、错过补跑/跳过判定、单任务互斥与运行记录 |
| `internal/future` | 会话引用、Worktree、策略、浏览器隔离数据和权限 |
| `internal/plugins`、`plugins` | 可信 Go 插件能力门禁、安装期摘要固化、开源声明读取与内嵌预装插件（Monitor、KCT、规划、定时任务） |
| `internal/updater` | GitHub Releases 检查、SHA-256 校验、跨平台安全自替换与重启 |
| `adapters` | 浏览器兼容 TypeScript 适配契约与夹具 |
| `web/src/host-entry.tsx` | 独立管理页与原生 surface 的可装配 ES 模块 |
| `internal/desktop/assets` | 宿主入口、面板/设置窗口、WebContentsView 与正式本地 RPC |
| `web`、`internal/webassets` | React 源码与 Go 内嵌资源 |
| `scripts`、`.github/workflows` | 构建、打包、验收、测量、CI 与自动发布 Release |

## 开源协议、上游声明与致谢

本项目以 [MIT License](LICENSE) 开源。每个平台的发布包都附带项目的 `LICENSE` 和依赖及上游声明 `THIRD_PARTY_NOTICES.txt`。在「KimiCode+ → 状态与诊断 → 开源许可」可查看第三方声明，在增强插件详情页可查看对应插件的 `LICENSE` 和 `NOTICE`。

### 上游适配说明

- **Monitor 增强插件（`kimicode-plus.monitor`）**：界面结构、样式、交互与中文文案基于开源项目 [bfjnbvf/kimi-code-monitor](https://github.com/bfjnbvf/kimi-code-monitor)（commit `1d529f0b424a708971cfac097238df444f3b5beb`，MIT License）二次适配，**感谢原作者十叶**。Rive 运行时与 `kimi_avatar_web-PnsTWI-X.riv` 沿用上游分发方式；余额数据层与核心通信接口为 KimiCode+ 独立实现（源码见 `internal/quota/`）。完整版权与说明见 [plugins/monitor/LICENSE](plugins/monitor/LICENSE) 与 [plugins/monitor/NOTICE](plugins/monitor/NOTICE)。如遇到本适配版的问题，请在本仓库提交 Issue。
- **思考内容翻译插件（KCT，`kimicode-plus.kct`）**：适配自独立版仓库 [wssz527/kimi-cot-translate](https://github.com/wssz527/kimi-cot-translate)（MIT License），将原有独立注入形态迁移为 KimiCode+ 插件运行时实现。完整说明见 [plugins/kct/LICENSE](plugins/kct/LICENSE) 与 [plugins/kct/NOTICE.md](plugins/kct/NOTICE.md)。

### 非官方与商标声明

KimiCode+ 是非官方社区开源工具，与 Moonshot AI / Kimi 无隶属、赞助或背书关系，不修改官方程序安装包。`Kimi`、`Kimi Code` 及 Monitor 中沿用的官方 Rive 蓝球形象均归其各自权利人所有，此处仅用于说明互操作与界面适配对象。

---

[插件开发文档](docs/PLUGINS.md) · [API 契约](docs/API_CONTRACT.md) · [架构](docs/ARCHITECTURE.md) · [验证结果与修复记录](docs/VALIDATION.md) · [原生接入门槛](docs/NATIVE_INTEGRATION.md)
