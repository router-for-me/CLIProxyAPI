# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts


<claude-mem-context>
# Memory Context

# [CLIProxyAPI] recent context, 2026-04-17 3:39pm GMT+8

Legend: 🎯session 🔴bugfix 🟣feature 🔄refactor ✅change 🔵discovery ⚖️decision
Format: ID TIME TYPE TITLE
Fetch details: get_observations([IDs]) | Search: mem-search skill

Stats: 50 obs (7,334t read) | 3,283,496t work | 100% savings

### Apr 17, 2026
57262 3:28p 🔵 聚合可用性把模型状态上升到凭证级别
57263 " 🔵 可用性筛选按优先级分组并取最高优先级
57264 " 🔵 SequentialFillSelector 采用粘性推进而非回跳
57265 " 🔵 可用凭证按优先级聚合并在同级内排序
57266 " 🔵 调度器把选择器语义映射到单/混合选路
57267 " 🔵 Scheduler 以优先级和分组视图重建 ready 池
57268 3:29p 🔵 scheduler 在单 provider 与混合 provider 间复用偏好
57269 " 🔵 混合调度器按最高优先级与轮转游标选路
57270 " 🔵 ready 选择先取最高优先级再按策略落点
57271 " 🔵 调度器以 selector 语义初始化状态容器
57272 " 🔵 模型分片按优先级与冷却状态重建
57273 " 🔵 调度器支持快路径与旧路径双实现
57274 " 🔵 快路径仅对内建 selector 开启
57275 3:30p 🔵 内建 selector 由类型白名单判定
57276 " 🔵 Manager 默认绑定轮转选择器并初始化调度器
57277 " 🔵 选择器参数在单路与混路中统一生成
57278 3:31p 🔵 内建选择器不接收 routeModel 参数
57279 " 🔵 测试覆盖了 sequential-fill、重试上限与冷却分支
57280 " 🔵 重试上限与模型别名已被测试覆盖
57281 " 🔵 disable_cooling 和 request-scoped 404 已有回归测试
57282 " 🔵 跨凭证重试上限已用测试锁定
57283 " 🔵 Unauthorized 回退测试覆盖 selector 与删除存储
57284 3:32p 🔵 README 记录了 Sequential Fill 的粘性与重试上限
57285 " 🔵 中文文档同步说明 Sequential Fill 粘性语义
57286 " 🔵 SequentialFillSelector 通过 RetryLimiter 限制为 2 次
57287 3:33p 🔵 已定位 SF 误限重试预算的根因
57288 " 🔵 RetryLimiter 接口定义在 conductor.go 中
57289 " 🔵 调度重试预算在两处读取 RetryLimiter
57290 " 🔵 SequentialFillSelector 把重试上限固定为 2
57291 " 🔵 README_CN 明确写出 SF 通过 2 次重试保持粘性
57292 3:35p ⚖️ 计划技能要求先写完整实施方案
57293 " ⚖️ 完成前必须先做验证
57294 3:37p 🔴 补上 SF 使用配置重试预算的回归测试
57295 " 🔴 补充 SF 配置重试预算回归测试
57296 " 🔵 新增回归测试已启动定向验证
57297 " 🔵 SF 回归测试首次运行失败
57298 3:38p 🔴 移除 SF 对外层重试预算的隐式限制
57299 " 🔴 Sequential Fill 不再限制外层重试预算
57300 " 🔴 移除 SF 对重试预算的隐式限制
57301 " 🔵 定向回归测试已再次启动
57302 " 🔵 仓库中已无 RetryLimiter 相关引用
57303 " 🔴 Sequential Fill 重试预算修复并验证通过
57304 " ✅ 格式化 Sequential Fill 重试修复相关文件
57305 " 🔴 修复 SF 对外层重试预算的误限制
57306 3:39p 🔵 全量 auth 测试已启动
**57307** " 🔵 **全量构建验证已启动**
已经开始对服务器入口做构建验证，但这次工具调用只拿到了运行中的会话 ID，没有最终输出。现阶段只能确认构建命令已执行，后续需要读取该 session 的完成结果才能判断修复后的代码是否仍可成功编译。
~69t 🔍 50,243

**57308** " 🔵 **auth 包全量测试已通过**
对整个 sdk/cliproxy/auth 包的全量回归验证已经完成，并且结果为通过。这说明移除 Sequential Fill 的 RetryLimiter 语义、恢复 request-retry 与 max-retry-credentials 标准计算之后，auth 包内的现有测试集没有出现新的失败。
~95t 🔍 47,871

**57309** " 🔵 **服务器构建验证已通过**
服务器入口的构建验证已经完成并通过，说明当前修复没有破坏 cmd/server 的编译。由于命令同时执行了构建和临时产物清理，这个结果也确认了修复后的代码能够在实际服务入口上顺利通过编译检查。
~65t 🔍 46,222

**57310** " 🔴 **修复 SF 误限重试预算并完成验证**
这次修复把 Sequential Fill 的职责收回到凭证选路本身，去掉了它通过 RetryLimiter 影响 Manager 外层重试预算的隐式行为。修复后，Manager 只按 request-retry 和 max-retry-credentials 计算重试，SF 不再把 MaxRetryAttempts=2 注入到 shouldRetry 决策里。为了防止回归，新增测试覆盖了 SF 在 429 配额路径下应遵循 request_retry=5 的行为；随后完成了定向测试、全量 auth 包测试和 cmd/server 构建验证，确认修复有效且未引入编译回归。README 和 README_CN 也已同步更新为标准重试语义。
~258t 🛠️ 45,256

**57311** " 🔵 **conductor.go 已恢复标准重试计算路径**
代码路径已经恢复到标准重试语义：Selector 只负责 Pick() 选路，不再携带任何重试限制接口；Manager 的 retrySettings() 直接读取 requestRetry 配置，closestCooldownWait() 也只以 requestRetry 作为默认值。这说明 Sequential Fill 之前影响外层重试预算的隐式通道已经移除，重试行为回到了统一的配置驱动模型。
~150t 🔍 44,757


Access 3283k tokens of past work via get_observations([IDs]) or mem-search skill.
</claude-mem-context>