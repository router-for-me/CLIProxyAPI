# CLIProxyAPI 维护流程优化记录

核验日期：2026-10-01。范围为本仓库的指令、项目技能、README 维护入口、
上游摄入工具、CI/Release 与 Homebrew 公式源；不修改全局技能、本机配置或 `.omo/`。
官方调研来源及适用范围见[维护指南](../maintenance.md#research-and-local-choices)。

## 覆盖与整改

| 对象 | 审阅范围 | 结果 |
| --- | --- | --- |
| `AGENTS.md` | 全文语义审阅 | 消除 translator 规则矛盾；按改动风险验证；修正实际 `/v8` module path；统一维护入口 |
| 项目技能 | 全文语义审阅、清单和 YAML 校验 | 改名 `cliproxyapi-fork-maintenance`；精确覆盖本 fork 的摄入、发布、安装/回退及流程修复；按任务加载手册 |
| `README.md` / `README_CN.md` / `README_JA.md` | 安装、维护、贡献段落语义审阅，全部 Markdown 本地引用检查 | 增加 fork 身份与一致安装入口；标注上游资料；PR 指向自有 `main` |
| README 提供商、赞助与生态介绍 | 保留内容，静态引用检查 | 未重新核验第三方产品、模型营销描述和外部 URL 的全部内容 |
| 中文迁移 runbook、`ops/` 流程 | 全文语义审阅 | 移除待迁移旧状态、固定 UID、`--tags` 和 `brew rollback`；改为权威流程导航 |
| 摄入脚本 | 实现审阅、隔离临时仓库测试 | 显式基线、稳定版本与发布时间验证、`--no-tags` 隔离、祖先关系检查 |
| 回归脚本 | 实现审阅、故障注入与完整实跑 | gofmt 错误传播、只检查 tracked Go、独立临时构建目录、纳入维护工具测试 |
| 两个 workflow | diff 与相关执行路径审阅、actionlint、离线发布失败路径测试 | 主干/tag 校验；草稿后发布；取消并发 checksum 写入；完整制品和远端哈希核验；固定 action SHA、最小权限 |
| Homebrew 公式及渲染器 | 实现审阅、Ruby/风格检查、输入故障测试 | 四平台同步更新；配置路径使用 `etc`；增加版本横幅测试；回退使用预先测试的版本化公式 |
| 主干 CI 格式兼容性 | 首次 `main` CI 日志、Go 1.26.0/1.26.4/1.27.1 复现、故障注入 | 修正三处既有格式；PR CI 与 Release 统一 Go 1.26.4；关卡使用活动工具链的 `gofmt` |

清单扫描发现本仓库一个 `AGENTS.md` 和一个项目 `SKILL.md`；扫描排除 `.git/`
及用户的 `.omo/`。清单本身是静态证据，语义审阅和脚本实测单独列出。

## 技能触发边界

名称包含项目和生命周期用途，描述使用“产品范围 + 实际工作 + 排除边界”，
避免通用 `sync` / `maintenance` 词单独抢占其他请求。

| 示例请求 | 预期 |
| --- | --- |
| 检查本 fork 可吸收的上游 release | 使用，读取摄入流程 |
| 发布本 fork 下一个版本 | 使用，读取发布流程；按已有授权执行 |
| 更新 tap、升级或回退本 fork | 使用，读取 Homebrew 流程 |
| 修复本仓库发布时校验清单不完整 | 使用，读取相关 workflow 和发布契约 |
| 修复普通 API 返回 400 或编写 Go 功能 | 不因该技能名而触发；按相应开发/调试流程 |
| 同步其他仓库或修改全局技能 | 不触发 |

以上是描述与正文的静态场景审阅；没有把它当作真实模型路由命中率实测。

## 验证与局限

- 9 个离线维护测试通过：四平台公式生成、非法/缺失/重复校验值、普通 tag
  隔离、非法参数与年龄门槛、未发布/未来发布时间、逆向基线、formatter
  执行失败，以及完整/缺失/损坏制品的发布行为。
- 完整回归关卡通过：维护工具测试、gofmt、go vet、responses-tools 不变量、
  全量 Go 测试、关键包 race 与 server build。首次主干 CI 随后暴露
  Go 1.27.1 与 1.26.x 的 `gofmt` 结果差异；三个既有文件已按发布工具链
  Go 1.26.4 格式化，PR CI 与 Release 现统一使用 1.26.4。
- `actionlint v1.7.12`、Bash 语法、Ruby 语法、技能 YAML 校验通过。
  Homebrew style 使用原样公式的临时 `Formula/` 副本通过；直接把 `ops/`
  副本当普通 Ruby 文件检查会套用非公式规则，不代表 tap 公式结果。
- 维护入口及三语言 README 的 64 个本地 Markdown 引用核验通过。

没有执行真实 GitHub 多平台 Release、tap 推送、Homebrew 安装/升级测试或服务重启。
发布权限、repository immutable releases、tag protection 和 attestations 尚需
对应远端配置/流程工作；SHA256 本身不证明来源。历史记忆的旧部署结论已写纠错记录，
重新检索仍可返回旧摘要，因此当前状态继续以仓库和实测证据为准。
