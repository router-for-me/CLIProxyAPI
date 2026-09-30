# CLIProxyAPI Fork 维护 Runbook

核验日期：2026-10-01。本文件保留维护决策及中文导航；当前操作步骤以
[维护指南](../maintenance.md)、[同步流程](../../ops/upstream-intake/README.md)、
[Homebrew 流程](../../ops/homebrew/README.md)为准，避免多处复制后发生漂移。

## 已采纳的维护决策

- 自主维护 `hrygo/CLIProxyAPI`，`main` 为主干；任务使用短期 `codex/*` 分支。
- `origin` 是我们的 fork，`upstream` 是 `router-for-me/CLIProxyAPI`。
- 上游只摄入已发布的稳定 release，按 `publishedAt` 等待至少 72 小时。
  显式指定评估基线，逐条择优 cherry-pick，并在基线与候选版本运行回归关卡。
- 上游 tag 仅取入 `refs/upstream/tags/*`，fetch 使用 `--no-tags`；
  不推 `--tags`、`--mirror` 或上游 refs。
- `go.mod` 保留上游 module path，降低移植成本；自有 `vX.Y.Z` 与上游独立编号。
- 发布经过完整回归、草稿、多平台制品、最终校验后再公开。已发布版本不覆盖；
  修复使用新版本。模型目录来自 tag，发布期间不拉取浮动目录改写源码。
- Homebrew tap 为 `hrygo/homebrew-cliproxyapi`，短名 `hrygo/cliproxyapi`。
  公式把归档内 `cli-proxy-api` 安装为 `cliproxyapi`，锁定版本 URL 和 SHA256。
- 安装更新与运行进程重启分别验证；配置与认证材料由原有配置链管理。

## 流程入口与验收

| 工作 | 入口 | 验收 |
| --- | --- | --- |
| 指令与开发 | [AGENTS.md](../../AGENTS.md) | 按改动风险执行验证，保留无关改动 |
| Agent 维护 | [cliproxyapi-fork-maintenance](../../.agents/skills/cliproxyapi-fork-maintenance/SKILL.md) | 项目与任务边界明确，按需加载流程 |
| 上游摄入 | [intake](../../ops/upstream-intake/README.md) | 显式基线、移植/跳过理由、基线/候选关卡证据 |
| 发版 | [maintenance](../maintenance.md) | workflow 全成功、完整 Release 和校验清单 |
| 公式与安装 | [homebrew](../../ops/homebrew/README.md) | 公式审阅、安装测试、磁盘与运行态分别核验 |

## 回退

`brew rollback` 不是有效的 Homebrew 回退流程。升级前准备并测试旧版的版本化公式，
可从 tap 历史用 `brew extract` 获取，再按安装流程 unlink/link；重启与配置兼容性
单独验证。不要假设旧 Cellar 目录一定保留，也不要手工覆盖 Homebrew 二进制。

## 历史与当前事实

最初的迁移方案包含将 13 个任务提交并入主干、发布首版 `v1.0.0`、建立 tap、
把手工安装切换到 Homebrew。它们是迁移背景，不是每次维护都应重复执行的待办。
当前仓库保留 `release.yaml` 与 `pr-test-build.yml`；上游专属工作流已移除。

旧资料中的固定 UID、进程状态、手工二进制路径状态和部署 SHA 仅是历史快照。
本轮没有核验远端 Release/tap 或本机运行服务，不能据仓库文件判断迁移及运行状态。
执行部署时读取当前服务管理器、配置路径、已安装版本和回退条件，不使用历史 PID/UID。

## 授权边界

优化文档、技能和流程不自动授权推送 tag、发布 tap、修改安装或重启服务。
已有同目标授权继续有效；流程内的命令本身不扩大授权。官方来源及本 fork 的本地
策略区分见[维护指南的调研部分](../maintenance.md#research-and-local-choices)。
