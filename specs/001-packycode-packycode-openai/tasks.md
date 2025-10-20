# Tasks: Packycode 提供商支持（代理至 Claude Code）

## Format: `[ID] [P?] [Story] Description`

- 任务必须使用如下格式：`- [ ] T001 [P] [US1] Description with file path`
- Setup/Foundational/Polish 阶段不加 [US?] 标签；用户故事阶段必须加 [US?]

## Path Conventions

- 所有文件路径为仓库根相对路径
- 创建新文件时在描述中使用确切目标路径

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 添加 Packycode 配置结构体到 internal/config/config.go（`type PackycodeConfig` 与 `Config.Packycode` 字段）
- [X] T002 在 internal/config/config.go 的 LoadConfigOptional 中设置 Packycode 默认值与调用 `sanitizePackycode(cfg)`
- [X] T003 在 internal/config/config.go 新增 `sanitizePackycode(cfg *Config)`，校验 `base-url` 非空、`wire-api=responses`、`privacy.disable-response-storage=true`、`requires-openai-auth` 与 `defaults` 合法性
- [X] T004 在 internal/api/server.go 的 UpdateClients 日志统计中加入 Packycode 客户端计数输出（与 codex/openai-compat 统计一致的风格）: `packycodeCount`
- [X] T005 在 internal/api/handlers/management/ 新建 `packycode.go`，实现 GET/PUT/PATCH 处理器，读写 `h.cfg.Packycode` 并持久化
- [X] T006 在 internal/api/server.go 的 registerManagementRoutes 中注册 `/v0/management/packycode` 的 GET/PUT/PATCH 路由

## Phase 2: Foundational (Blocking Prerequisites)

- [X] T007 在 internal/watcher/watcher.go 的 SnapshotCoreAuths 中基于 `cfg.Packycode` 合成一个 coreauth.Auth：`Provider=codex`，`Attributes.api_key=openai-api-key`，`Attributes.base_url=packycode.base-url`
- [X] T008 在 internal/watcher/watcher.go 的 diff/变更摘要中加入 Packycode 相关变化提示（例如 `packycode.enabled/base-url/...`），与现有输出风格一致
- [X] T009 在 README_CN.md 的配置章节追加 `packycode:` 字段示例与说明（参考 specs/001-packycode-packycode-openai/quickstart.md）
- [X] T010 在 MANAGEMENT_API_CN.md/MD 中追加 `/v0/management/packycode` 端点说明（GET/PUT/PATCH），字段与默认值说明；同步英文版 MANAGEMENT_API.md

- [X] T027 新增 CLI 标志以注册 Packycode 模型：
  - 在 `cmd/server/main.go` 增加 `--packycode`（或短别名）布尔标志
  - 行为：当检测到 `cfg.Packycode.enabled=true` 且 `base-url`、`openai-api-key` 合法时，主动将 OpenAI/GPT 模型（如 `gpt-5`、`gpt-5-*`、`gpt-5-codex-*`、`codex-mini-latest`）注册进全局 ModelRegistry（provider 归属 `codex`）
  - 要求：执行时不依赖文件变更事件；若与正常服务一同启动，则在服务启动钩子后立即生效
  - 错误处理：若 `packycode` 配置不完整或校验失败，输出清晰错误并返回非零码

- [X] T028 在服务启动路径补充 Packycode 模型注册的兜底钩子：
  - 在 `sdk/cliproxy/service.go` 的启动/重载回调中，当 `cfg.Packycode.enabled=true` 时，直接调用 ModelRegistry 注册 OpenAI 模型（同 T027 逻辑），确保 `/v1/models` 可见 `gpt-5` 等模型
  - 要求：与 Watcher 的合成 Auth 搭配工作；重复注册需幂等处理（使用稳定 clientID，例如基于 `packycode:codex:<base-url|api-key>` 的短哈希）

## Phase 3: User Story 1 - 启用 Packycode 并成功转接 (Priority: P1) 🎯 MVP

- 独立验收：`config.yaml` 新增 `packycode` 字段并启用后，经 Claude Code 兼容入口发起一次请求，收到有效响应

### Implementation for User Story 1

- [X] T011 [US1] 在 internal/config/config.go 定义 Packycode 配置字段：
  - enabled(bool)、base-url(string, required)、requires-openai-auth(bool, default true)、wire-api(string, fixed "responses")、privacy.disable-response-storage(default true)、defaults.model/defaults.model-reasoning-effort
- [X] T012 [US1] 在 internal/api/handlers/management/packycode.go 实现 `GetPackycode/PutPackycode/PatchPackycode`，调用 `h.persist(c)` 并支持只读 `effective-source`
- [X] T013 [US1] 在 internal/api/server.go 注册路由：`mgmt.GET/PUT/PATCH("/packycode", ...)`
- [X] T014 [US1] 在 internal/watcher/watcher.go 依据 `cfg.Packycode.enabled` 决定是否合成 `coreauth.Auth`，并为其生成稳定 ID（使用现有 idGen）
- [X] T015 [US1] 在 internal/runtime/executor/codex_executor.go 无需改动；通过 watcher 合成的 `Provider=codex` + `base_url` 指向 Packycode 即可直通
- [X] T016 [US1] 在 README_CN.md 增加“使用 Packycode”快速验证步骤（参考 specs/.../quickstart.md）

## Phase 4: User Story 2 - 配置校验与可执行报错 (Priority: P2)

- 独立验收：缺失/无效上游密钥或必填项时，保存被拒并获得可执行修复提示

### Implementation for User Story 2

- [X] T017 [US2] 在 internal/api/handlers/management/packycode.go 的 PUT/PATCH 中做字段校验（base-url 必填、requires-openai-auth=>openai-api-key 必填、wire-api=responses、effort 枚举）并返回 422 with 错误详情
- [X] T018 [US2] 在 internal/config/config.go 的 `sanitizePackycode` 中补充严格校验，返回清晰错误（LoadConfigOptional 时可选→错误提示）
- [X] T019 [US2] 在 docs 与 README_CN.md 提示常见错误与修复（缺密钥/URL/非法 effort）

## Phase 5: User Story 3 - 回退与降级 (Priority: P3)

- 独立验收：Packycode 不可用时，可快速停用并恢复至其他已配置提供商，或向调用方输出明确错误

### Implementation for User Story 3

- [X] T020 [US3] 在 internal/watcher/watcher.go 中，当 `packycode.enabled=false` 时移除对应合成的 Auth（触发 rebindExecutors）
- [X] T021 [US3] 在 internal/runtime/executor/codex_executor.go 的错误分支日志中增强可读性（保留现有输出格式，不含用户内容）
- [X] T022 [US3] 在 README_CN.md 增加“快速停用/恢复”说明与故障定位建议

## Phase N: Polish & Cross-Cutting Concerns

- [ ] T023 [P] 补充 MANAGEMENT_API.md 与 MANAGEMENT_API_CN.md 的示例请求/响应样例（与 contracts/management-packycode.yaml 一致）
- [ ] T024 [P] 在 config.example.yaml 添加 `packycode:` 示例片段（注释形式，与现有风格一致）
- [ ] T025 在 internal/api/handlers/management/config_lists.go 附近增加注释引用新的 packycode 管理文件，便于维护者发现
- [ ] T026 在 .codex/prompts/speckit.* 中如有对 codex/codex-api-key 的文字，增加 Packycode 说明（不改变行为）

## Dependencies & Execution Order

### Phase Dependencies

- Phase 1 → Phase 2 → Phase 3 (US1) → Phase 4 (US2) → Phase 5 (US3) → Polish

### User Story Dependencies

- US1 无依赖（MVP）
- US2 依赖 US1 的配置与接口就绪（校验与错误返回覆盖 PUT/PATCH）
- US3 依赖 US1 的启用路径（用于回退/降级验证）

### Within Each User Story

- 合同/管理接口 → 配置→ 路由/合成 Auth → 文档

## Parallel Opportunities

- [P] T005 与 T006 可并行（管理处理器与路由注册分文件修改）
- [P] T001/T002/T003 与 T004 可并行（配置结构/校验与日志统计分别修改）
- [P] 文档类任务（T009/T010/T016/T019/T022/T023/T024/T026）可并行

## Implementation Strategy

### MVP First (User Story 1 Only)

- 完成 T001–T006、T007、T011–T016 后即可验收 US1

### Incremental Delivery

- US2 增强校验与错误消息（T017–T019）
- US3 降级策略与文档（T020–T022）

### Parallel Team Strategy

- 一人负责管理接口与路由（T005/T006/T012/T013/T017）
- 一人负责配置/合成与运行时（T001–T004/T007/T014/T015/T020/T021）
- 一人负责文档与示例（T009/T010/T016/T019/T022/T023/T024/T026）

## Notes

- 所有新增/修改需遵守“隐私优先与最小化留存”：不持久化用户内容；日志仅记录必要元信息
- 合同变更与实现需保持一致（contracts/management-packycode.yaml）
