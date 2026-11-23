# KimiCode+ 项目协作与发布规范

## 发布新版本与自动更新契约

客户端默认按“国内 CNB 镜像优先、GitHub 兜底”的顺序通过固定下载入口检查与下载更新，不依赖限流的 GitHub 匿名 REST API（`v0.2.3` 仅默认 GitHub 清单入口，双源切换随后续版本生效）：

```text
https://cnb.cool/wssz527/kimicode-plus/-/releases/latest/download/manifest.json
https://github.com/wssz527/kimicode-plus/releases/latest/download/manifest.json
```

客户端读取 `manifest.json` 获取最新版本号、发布时间、更新说明与各平台归档名，再从同一发布源的 `download/v<version>/` 下载对应平台压缩包及 `SHA256SUMS` 完成 SHA-256 校验与自替换；若优先源检查或下载失败，自动回退到备用源并同样执行完整校验（实现见 `internal/updater/updater.go`、`internal/updater/apply.go`、`internal/updater/archive.go`）。

### 标准发版流程（优先采用）

1. 将 `internal/server/server.go` 中的 `var Version = "<X.Y.Z>"` 更新为新版本号（语义化版本，不含 `v` 前缀）。
2. 完成下方全部验证（前端检查与构建、嵌入资产、Go 检查与测试、打包与同步测试、真实二进制验收）。
3. 提交发布改动，**并将面向用户的更新说明写入该发布 commit 的提交信息**。`.github/workflows/release.yml` 打包时会读取 `git log -1 --format=%B` 写入 `manifest.json` 的 `releaseNotes`，读取 `git log -1 --format=%cI` 写入 `publishedAt`。后续仅在 GitHub/CNB 网页编辑正文不会同步回 `manifest.json`。
4. 基于该提交打注释标签 `v<X.Y.Z>` 并推送到远端，由 GitHub Actions（`.github/workflows/release.yml`）自动完成构建、交叉编译、打包、发布 GitHub Release，并通过 `scripts/sync_cnb_release.py`（依赖 GitHub 仓库 Secret `CNB_TOKEN`，权限限定为 `wssz527/kimicode-plus` 的 `repo-code` + `repo-release` 读写）自动同步推送到 CNB 镜像仓库 `wssz527/kimicode-plus`。禁止覆盖已有 tag 的 Release。

### 自动更新生效的硬性要求（缺一不可）

无论是自动发版后核验，还是特殊情况下手工创建/补传 Release，GitHub 与 CNB 镜像源必须同时满足以下条件：

- **仓库与 Release 状态**：
  - GitHub 仓库（`wssz527/kimicode-plus`）与 CNB 镜像仓库（`wssz527/kimicode-plus`）均必须保持 Public，否则匿名拉取清单和安装包会失败。
  - Tag 必须严格命名为 `v<X.Y.Z>`，且与 `manifest.json` 中每条记录的 `version`（`<X.Y.Z>`）一致。
  - 正式版必须是非草稿（non-draft）且非预发布（`prerelease: false`）。带 `-beta.1`、`-rc.1` 等预发布后缀的标签会发布为 Pre-release，不会被 `latest/download/manifest.json` 指向。
- **必须上传的 6 个核心资产**（严禁只替换单个安装包而不同步更新 `manifest.json` 和 `SHA256SUMS`；上传 CNB 时须将 `manifest.json` 放在最后上传，确保清单可见时全部归档与校验文件已就位）：
  1. `manifest.json`：由 `scripts/package.py` 生成；所有条目 `version` 必须一致且与 tag 匹配。缺少该文件时，固定入口会返回 404，客户端会视为“尚未发布正式 Release 版本”而不会提示更新。
  2. `SHA256SUMS`：包含全部发布压缩包的 64 位小写 SHA-256 摘要，格式须匹配 `^([a-f0-9]{64}) [ *]([^/\\\s]+)$` 且无重复项。缺少或摘要不一致会拒绝安装。
  3. `enhanced-kimi-<X.Y.Z>-darwin-arm64.tar.gz`
  4. `enhanced-kimi-<X.Y.Z>-darwin-amd64.tar.gz`
  5. `enhanced-kimi-<X.Y.Z>-windows-amd64.zip`
  6. `THIRD_PARTY_NOTICES.txt`
- **归档内部要求**：每个压缩包必须小于 128 MiB，不含非法路径或符号链接，且包含对应平台的可执行文件（`enhanced-kimi-<os>-<arch>` / `.exe` 或 `enhanced-kimi` / `.exe`，二进制格式与目标系统匹配）、`LICENSE`、`THIRD_PARTY_NOTICES.txt` 与 `RUN.txt`。

### 手工打包与补传规则（仅在无法使用 GitHub Actions 时）

`scripts/crossbuild.sh` 只执行 Go 交叉编译，**不会**重新构建或嵌入前端静态资源。本地打包前必须先构建并嵌入前端（可直接先运行 `sh scripts/build.sh`），再按顺序生成发布产物：

```sh
sh scripts/build.sh
VERSION=<X.Y.Z> sh scripts/crossbuild.sh
python3 scripts/notices.py
RELEASE_PUBLISHED_AT="<ISO8601时间>" RELEASE_NOTES="<更新说明>" python3 scripts/package.py --version <X.Y.Z>
```

手工在 GitHub 创建 Release 时，必须先保存为 Draft，将 `dist/release/*.tar.gz`、`dist/release/*.zip`、`dist/release/SHA256SUMS`、`dist/release/manifest.json` 与 `dist/THIRD_PARTY_NOTICES.txt` 全部上传完整后再点击 Publish；同步到 CNB 时可使用 `CNB_TOKEN=... python3 scripts/sync_cnb_release.py --tag v<X.Y.Z> ...`（该脚本会自动按“归档 → `SHA256SUMS` → `manifest.json`”顺序上传）。

## 官方插件独立在线更新与发布契约

从 `v0.2.8` 起，官方预装插件（`kimicode-plus.kct`、`kimicode-plus.monitor`、`kimicode-plus.planner`、`kimicode-plus.scheduler`）支持独立于主程序发版并热更新，无需重装核心或重启 Kimi Code。

客户端采用与核心一致的“CNB 镜像优先、GitHub 兜底”双源策略，从独立的非 latest 固定通道标签 `plugins-latest` 拉取内嵌 Ed25519 签名的 `plugins-manifest.json`（不影响主程序 `releases/latest`）：

```text
https://cnb.cool/wssz527/kimicode-plus/-/releases/download/plugins-latest/plugins-manifest.json
https://github.com/wssz527/kimicode-plus/releases/download/plugins-latest/plugins-manifest.json
```

### 独立发布官方插件流程

1. 修改 `plugins/<name>/` 下的插件代码并递增该插件 `plugin.json` 的 `version`。
2. 推送 `plugin-*` 标签（如 `plugin-kct-v1.0.5`）或在 GitHub Actions 手动运行 `Release Official Plugins`（`.github/workflows/release-plugins.yml`）。
3. 工作流使用 GitHub 仓库 Secret `PLUGIN_SIGNING_PRIVATE_KEY`（与仓库 `plugins/official_keys.go` 内置公钥配对的 Ed25519 私钥，禁止提交到仓库）执行：
   - `go run ./cmd/plugin-release package` 打包并签名生成 `*.zip` 与 `plugins-manifest.json`；
   - `go run ./cmd/plugin-release verify` 用内置公钥反向校验签名与每个压缩包的 SHA-256 及目录内容摘要；
   - 先上传各版本化 `*.zip`，最后原子覆盖上传 `plugins-manifest.json` 到 GitHub `plugins-latest`（`--latest=false`），再通过 `scripts/sync_cnb_release.py --tag plugins-latest --no-prerelease --make-latest false --force-tag` 同步到 CNB 镜像。
4. 客户端检查更新时校验证书公钥签名、最低核心版本（`minCoreVersion`）和权限变更：能力未扩权可直接一键热更新，新增能力会要求用户明确确认；已验签清单随插件记录持久化，v2 高权限插件更新及重启后均保持官方签名校验有效。

## 日常开发与验证

- 隔离验收必须使用 `work/` 下的独立 `--data-dir`、`--host-profile` 与 `--kimi-home`，避免触碰日常配置；中间产物放入 `work/`，不纳入 git 提交。
- 发布前标准检查命令：

```sh
(cd web && npm run typecheck && npm test && npm run build && npm run build:host)
sh scripts/build.sh
go vet ./cmd/... ./internal/... ./plugins/...
go test ./cmd/... ./internal/... ./plugins/... -count=1
python3 scripts/test_package.py
python3 scripts/test_sync_cnb_release.py
python3 scripts/acceptance.py --binary ./dist/enhanced-kimi
```
