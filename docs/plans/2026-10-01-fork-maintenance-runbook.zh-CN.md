# CLIProxyAPI Fork 维护 Runbook

状态：已采纳，实施中。核验时间 2026-10-01。

## 结论

本仓库（`hrygo/CLIProxyAPI`）是自维护 fork，`main` 为主干。对上游
`router-for-me/CLIProxyAPI` 只跟踪其 release 版本，按 release 择优吸收，不再
以 `upstream/dev` 为 rebase 基线，也不再等待上游合并。制品由本 fork 的 GitHub
Release 产出，本机通过 Homebrew tap 安装，取代手工 `go build` + `mv` 覆盖二进制。

`go.mod` 保持 `module github.com/router-for-me/CLIProxyAPI` 不变，使上游 release
可直接移植而不必全仓改 import。

远端约定：`origin` = `hrygo/CLIProxyAPI`（我们的 fork），
`upstream` = `router-for-me/CLIProxyAPI`（上游，只读吸收源）。
本 fork 首个版本为 `v1.0.0`。

## 已确认的仓库事实

- `.github/workflows/release.yaml` 是 fork 无关的：发布目标为
  `${{ github.repository }}`，权限用自动注入的 `GITHUB_TOKEN`。因此 fork 继承
  该流水线后，打 tag 即产出本 fork 的 Release 制品，无需重写。触发条件已从
  任意 tag 收紧为 `v[0-9]+.[0-9]+.[0-9]+`，并加了 concurrency 组避免同 tag
  重复发布。
- macOS arm64 制品名：`CLIProxyAPI_<version>_darwin_aarch64.tar.gz`；
  amd64 为 `CLIProxyAPI_<version>_darwin_amd64.tar.gz`。产物内二进制名
  `cli-proxy-api`，同包含 `LICENSE`、`README.md`、`README_CN.md`、
  `config.example.yaml`，Release 另附 `checksums.txt`。
- 迁移前本机 `/opt/homebrew/bin/cliproxyapi` 是**普通文件**（不在 Cellar，
  `brew list` 不可见），即历史手工 `mv` 的遗留；配置在
  `/opt/homebrew/etc/cliproxyapi.conf`。
- 运行态由 launchd `com.hrygo.cliproxyapi` 拉起，参数为
  `/opt/homebrew/bin/cliproxyapi -config /opt/homebrew/etc/cliproxyapi.conf`，
  `RunAtLoad` + `KeepAlive`，日志在 `~/.cli-proxy-api/logs/`。

## 分支与主干

- `main` 为主干与发布基线。`codex/*` 为任务分支，完成后合回 `main`。
- 现有 13 个分支提交需并入 `main`。
- 合并完成后，历史任务分支 `codex/responses-tools-core` 归零，只作历史保留，
  不再作为部署来源。

## 上游 release 摄入

上游 tag 只能取到本地命名空间，**绝不能推到 `origin`**：`release.yaml` 对
`v*` tag 推送会触发整套多平台构建并发布 Release。上游 tag 的版本号通常也是
`vX.Y.Z`，一旦推送就会从我们的 fork 产生一批虚假 release。

按需摄入单个上游 tag：

```bash
git fetch upstream refs/tags/vX.Y.Z:refs/upstream/tags/vX.Y.Z
git log --oneline refs/upstream/tags/vPrev..refs/upstream/tags/vX.Y.Z
```

筛选准则：协议与客户端兼容类修复优先；行为变更、配置语义变更、上游 CI/构建
调整默认不吸收；翻译层与注册表的大规模重构先评估再定。吸收方式以 cherry-pick
单个提交为主，避免整段 merge 带入无关改动。吸收后打我们自己的 `vX.Y.Z`。

## 发布

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin main --tags   # 仅推我们自己的 tag
```

`release.yaml` 会产出 GitHub Release 与各平台 tarball/zip 及 `checksums.txt`。

## 安装（Homebrew tap）

tap 仓库为 `hrygo/homebrew-cliproxyapi`，公式 `cli-proxy-api.rb` 指向本 fork 的
release tarball 并锁 `sha256`。

```bash
brew tap hrygo/tap                 # 映射到 hrygo/homebrew-cliproxyapi
brew install hrygo/tap/cli-proxy-api
brew upgrade cli-proxy-api
brew rollback cli-proxy-api        # 回退上一版本
```

版本号须与 release tag 的 `RELEASE_VERSION`（tag 去掉前导 `v`）一致，否则
Homebrew 判定为陈旧版本。

### 从手工安装切换

切换存在一个二进制交接窗口：运行中进程在重载前仍持有旧 inode，不算停机，但需
选定时机。

```bash
cp -p /opt/homebrew/bin/cliproxyapi ~/cliproxyapi.manual.bak   # 备份手工二进制
brew install hrygo/tap/cli-proxy-api                            # 装 tap 版本
launchctl kickstart -k gui/501/com.hrygo.cliproxyapi             # 重载新二进制
/opt/homebrew/bin/cliproxyapi -version                          # 核对版本与 commit
```

若 brew 安装因路径被手工文件占用而失败，先把该文件移出 `/opt/homebrew/bin`，
再执行 `brew install`，随后 `launchctl kickstart -k`。

## 版本核对

```bash
/opt/homebrew/bin/cliproxyapi -version   # 期望 Version=<release_version> Commit=<short sha>
```

## fork 工作流清理

已完成，保留与移除如下：

保留：

- `release.yaml`：产出 Release 制品。触发条件已收紧为 `v[0-9]+.[0-9]+.[0-9]+`，
  并加 concurrency 组。
- `pr-test-build.yml`：改为在 PR 与 `main` 推送时运行，校验 `gofmt`、
  `go vet ./...`、`go test ./... -count=1` 与 server build。

移除：

- `docker-image.yml`：向第三方 Docker Hub 组织 `eceasy/cli-proxy-api` 推送镜像，
  与我们无关且依赖 fork 中不存在的 secrets。
- `auto-retarget-main-pr-to-dev.yml`：上游 PR 流程，面向上游 `dev` 基线。
- `agents-md-guard.yml`：自动关闭一切改动 `AGENTS.md` 的 PR，与我们自行维护
  `AGENTS.md` 的流程冲突。
- `pr-path-guard.yml`：禁止 PR 改动 `internal/translator/`。这条限制来自上游维护
  模式；我们已拥有自己的 fork，AGENTS.md 中对应的限制已同步移除。
- `.github/FUNDING.yml`：指向 `router-for-me` 的赞助配置。

注意 `refresh-model-catalogs.sh` 仍默认拉取 `router-for-me/models.git` 的模型
目录。这是数据而非代码，保留有利于我们及时获得上游模型清单。

## 回退

- 代码层：每个发布前打本地 tag 作为恢复点，回退以 tag 指向的提交为准。
- 制品层：`brew rollback cli-proxy-api`。
- 运行态：回退后需 `launchctl kickstart -k` 才会真正换进程。

磁盘二进制、运行中进程、Release 状态与版本号是四个独立事实，不可互相推断。

## 授权边界

切二进制、重启 launchd、推送 tag 或创建 tap 仓库属于远端/运行态变更，须对应
任务的明确授权。本 runbook 描述流程本身不构成执行授权。
