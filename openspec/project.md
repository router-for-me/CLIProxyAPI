# Project Context

## Purpose
CLIProxyAPI 是一个为本地与容器环境提供 OpenAI/Gemini/Claude/Codex 兼容接口的代理服务，面向各类 CLI 编码工具与 OpenAI‑兼容 SDK。项目目标：
- 统一 /v1 OpenAI 兼容与 /v1/messages（Claude）以及 /v1beta（Gemini）接口，减少上游差异带来的集成成本。
- 提供多账户负载均衡与可选上游（OpenAI 兼容、Packycode 等），提升可用性与弹性。
- 提供管理 API（开关/修改关键配置、日志与用量可视化）与热更新能力，降低运维复杂度。

## Tech Stack
- 语言与版本：Go 1.24（go.mod 指定）
- Web 框架：Gin v1.10（路由与中间件）
- 日志：logrus + 可选文件滚动（lumberjack）
- 存储选项（配置与令牌持久化）：
  - PostgreSQL（pgx）
  - Git 仓库（go-git）
  - S3 兼容对象存储（minio-go）
- 构建与分发：GoReleaser（.goreleaser.yml）、Docker 多阶段构建、docker-compose
- CI/CD：GitHub Actions（release、docker 镜像多架构构建与推送）
- 其他依赖：fsnotify（热重载）、oauth2、gjson/sjson、x/net、x/crypto 等

## Project Conventions

### Code Style
- gofmt/goimports 统一格式；包名小写、聚焦单一职责。
- 错误优先返回；严禁吞错；关键路径使用 `context` 与超时控制。
- 统一使用 logrus 分级日志；是否写入文件由配置 `logging-to-file` 控制。
- 配置源为 YAML 文件；管理端修改持久化回写；环境变量作增量覆盖。
- 请求日志与 TPS 采样为可配置中间件，默认开启按需切换（见下“Architecture Patterns”）。

### Architecture Patterns
- 目录结构与职责
  - `cmd/server/` 进程入口，组装配置/存储/服务并启动 HTTP 服务（cmd/server/main.go:48）。
  - `internal/api/` HTTP 服务实现，基于 Gin 的路由、中间件与各 Provider 处理器（internal/api/server.go:311）。
  - `sdk/` 可复用的处理器与访问层，供嵌入式或二次开发使用。
  - `internal/*` 其余为配置解析、日志、访问控制、统计、管理面板等模块化实现。
- 中间件
  - 访问日志与 panic 恢复：`logging.GinLogrusLogger/GinLogrusRecovery`（internal/api/server.go:189）。
  - CORS：允许常见跨域（internal/api/server.go:694）。
  - 统一鉴权中间件：基于 `sdk/access.Manager`，支持多种凭据来源（internal/api/server.go:860）。
  - 每请求 TPS 采样：按 /v1 路径落点汇总（internal/api/server.go:239）。
- 接口路由
  - OpenAI 兼容：`/v1/models`、`/v1/chat/completions`、`/v1/completions`、`/v1/responses`。
  - Claude Code：`/v1/messages`、`/v1/messages/count_tokens`。
  - Gemini 兼容：`/v1beta/models` 与 `:action` 路由。
  - 管理端：`/v0/management/**`（启用后注册，可热切换）。
- 配置与热更新
  - 支持本地文件、PostgreSQL、Git 仓库、对象存储四种后端；启动期自动 Bootstrap（cmd/server/main.go:214）。
  - 管理端或文件变更触发热更新与客户端重建，且动态切换日志与统计开关（internal/api/server.go:718）。

### Testing Strategy
- 文档驱动测试与复现：项目采用 `project-history-plan/3-test` 流程与命令工具来记录和验证复现策略（.claude/commands/test.md）。
- 单元/集成测试（规划建议）：
  - 优先为中间件与路由编排补充表驱动测试（internal/api/server.go 的 CORS、鉴权、路由分发）。
  - 针对配置热更新路径编写最小子集测试，验证日志开关、统计开关、管理路由注册切换。
  - 外部提供方调用以 fake 客户端/接口契约测试为主，避免真实上游依赖。
- 执行策略：仅运行与改动直接相关的最小子集；确有必要再扩到子树；避免全量。

### Git Workflow
- 分支：`main` 为稳定分支，`feature/*`、`fix/*` 进行开发。
- 提交规范：Conventional Commits（feat/fix/refactor/docs/chore/test/build/ci）。
- 语义化版本：通过打 tag 触发 GoReleaser 发布（.github/workflows/release.yaml）。
- 镜像发布：`v*` 标签触发多架构 Docker 构建与推送（.github/workflows/docker-image.yml）。

## Domain Context
- 支持的上游/协议：OpenAI 兼容（/v1）、Claude Messages（/v1/messages）、Gemini（/v1beta）。
- 认证登录：支持 Gemini/OpenAI/Claude/Qwen/iFlow 的本地 OAuth 登录流程；各自监听端口：8085/1455/54545/—/11451（README.md）。
- 多账户负载均衡：同一 Provider 多账户轮询；部分能力仅限本地回环以确保安全（README.md “NOTE”）。
- 管理 API：`/v0/management/**` 提供配置读写、日志、用量与密钥管理；启用条件为配置或环境提供管理密钥（internal/api/server.go:291）。
- 可选 Packycode：对外 provider=packycode，对内映射至 Codex 执行器与 OpenAI 模型集；/v1/models 可见，管理端可列举/筛选（参见 openspec/changes/tps-specified-model/specs/provider-integration/packycode-provider-alias.md）。

## Important Constraints
- 安全缺省：
  - 远程管理默认关闭（`remote-management.allow-remote=false`）；无密钥时管理接口整体 404。
  - 管理密钥可由配置或环境变量注入；启用后才注册管理路由。
  - Gemini CLI 相关端点限制本地访问，避免未鉴权的远程调用。
- 日志与合规：支持按配置切换文件落盘；请求日志与 TPS 采样可控，避免过量持久化。
- 部署与端口：服务端口默认 53355；OAuth 回调端口分别用于登录；容器镜像为 Alpine 运行时，多架构（amd64/arm64）。
- 资源与伸缩：上游配额耗尽时可按配置切换项目与 preview 模型（config.example.yaml）。

## External Dependencies
- 上游 AI 服务：
  - Google Gemini（官方 Generative Language API 或 CLI OAuth）
  - OpenAI（GPT/Codex，经 OAuth 或 OpenAI‑兼容上游）
  - Anthropic Claude（OAuth/messages）
  - Qwen、iFlow 生态模型
  - OpenAI 兼容上游（如通过 `openai-compatibility` 配置的提供商）
- 可选持久化后端：PostgreSQL、S3 兼容对象存储、远端 Git 仓库
- CI/CD 与分发：GitHub Actions、Docker Hub、Homebrew 公式
