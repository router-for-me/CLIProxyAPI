# Feature Specification: Packycode 提供商支持（代理至 Claude Code）

**Feature Branch**: `001-packycode-packycode-openai`  
**Created**: 2025-10-20  
**Status**: Draft  
**Input**: User description: "为这个项目增加packycode支持；packycode 是一个openai codex gpt5的第三方提供商；使用packycode，需要设置以 下变量：<变量>cat > ~/.codex/config.toml << EOF model_provider = packycode model = gpt-5 model_reasoning_effort = high disable_response_storage = true [model_providers.packycode] name = packycode base_url = https://codex-api.packycode.com/v1 wire_api = responses requires_openai_auth = true EOF</变量><变量2>cat > ~/.codex/auth.json << EOF { OPENAI_API_KEY: sk-Kvn0G...7sS } EOF</变量2>；packycode的这些设置，只支持codex cli使用；通过为 CLIProxyAPI项目增加packycode支持，可以将packycode的提供的服务， 代理转接到claude code中"

## User Scenarios & Testing *(mandatory)*

<!--
  IMPORTANT: User stories should be PRIORITIZED as user journeys ordered by importance.
  Each user story/journey must be INDEPENDENTLY TESTABLE - meaning if you implement just ONE of them,
  you should still have a viable MVP (Minimum Viable Product) that delivers value.
  
  Assign priorities (P1, P2, P3, etc.) to each story, where P1 is the most critical.
  Think of each story as a standalone slice of functionality that can be:
  - Developed independently
  - Tested independently
  - Deployed independently
  - Demonstrated to users independently
-->

### User Story 1 - 启用 Packycode 并成功转接 (Priority: P1)

作为 CLIProxyAPI 的运维/集成者，我可以在系统中选择并启用 Packycode 作为后端提供商，使开发者继续通过 Claude Code 兼容入口进行使用，并获得完整、无感的响应。

**Why this priority**: 解锁新的上游能力与价格/性能选择，且对现有用户入口无感知变更，业务价值与影响面最大。

**Independent Test**: 仅通过配置与一次标准调用即可验证：完成 Packycode 启用 → 通过现有 Claude Code 兼容入口发起一次代码助理请求 → 收到有效响应。

**Acceptance Scenarios**：

1. **Given** 已具备可用的上游凭证，**When** 在系统中选择 Packycode 并保存配置，**Then** 后续请求通过 Packycode 转接并正常返回结果。
2. **Given** 已启用 Packycode，**When** 通过 Claude Code 兼容入口发起典型代码助理请求（如补全、解释），**Then** 用户获得完整且连贯的响应。
3. **Given** 在 `config.yaml` 中新增并配置了 `packycode` 字段（包含 Packycode 所需的全部选项，如 `base-url`、`requires-openai-auth`、`wire-api`、隐私与默认模型相关项），且提供了可用的上游 OpenAI 密钥，**When** 重载配置，**Then** 请求可无感转接至 Packycode 并成功返回。

---

### User Story 2 - 配置校验与可执行报错 (Priority: P2)

作为配置者，在缺失或无效上游凭证/必填项时，我能获得明确且可执行的修复提示，避免反复试错。

**Why this priority**: 降低集成成本与支持成本，提升一次配置成功率。

**Independent Test**: 仅通过尝试保存错误配置并查看系统提示，即可独立验证。

**Acceptance Scenarios**：

1. **Given** 缺失必要的上游凭证，**When** 尝试启用 Packycode，**Then** 系统给出指向性强的修复指引并拒绝启用。
2. **Given** 必填项格式不正确，**When** 保存配置，**Then** 系统在保存前即提示问题来源与修正方式。

---

### User Story 3 - 回退与降级 (Priority: P3)

作为运维者，当 Packycode 上游不可用或不稳定时，我可以快速停用该提供商并恢复到既有可用提供商或清晰地向调用方报错，系统整体保持稳定。

**Why this priority**: 保障可用性，避免单点上游导致整体不可用。

**Independent Test**: 模拟上游不可用，验证停用/恢复路径与调用方的错误可读性。

**Acceptance Scenarios**：

1. **Given** 正在使用 Packycode，**When** 上游故障，**Then** 系统允许快速停用并恢复至既有可用提供商（若已配置）。
2. **Given** 正在使用 Packycode，**When** 无可用替代提供商，**Then** 调用方收到明确、可读的错误而非系统崩溃。

---

[Add more user stories as needed, each with an assigned priority]

### Edge Cases

<!--
  ACTION REQUIRED: The content in this section represents placeholders.
  Fill them out with the right edge cases.
-->

- 上游限流或配额耗尽：请求被拒绝时向调用方输出清晰的原因与下一步建议。
- 上游模型或能力暂不可用：提示兼容范围与可替代方案（如切换默认能力）。
- 网络超时/中断：提供可重试/取消的用户路径与明确反馈。
- 超长请求/响应：对超出允许范围的负载给出提前告知与分段/截断策略说明（面向用户层面的期望管理）。
- 隐私默认开启：任何情况下代理端均不持久化用户请求与响应内容。
- 配置来源冲突：当同时存在 `config.yaml` 的 `packycode` 字段与 Codex CLI 的 Packycode 配置文件时，采用明确的优先级策略（默认以 `config.yaml` 为最高优先级），并提示生效来源。
- 缺失 OpenAI 上游密钥：当 Packycode 需要上游 OpenAI 密钥但未提供时，阻止启用并给出修复步骤。

## Requirements *(mandatory)*

<!--
  ACTION REQUIRED: The content in this section represents placeholders.
  Fill them out with the right functional requirements.
-->

### Functional Requirements

- **FR-001**: 系统必须允许在 CLIProxyAPI 中将“Packycode”配置为可选提供商，并能启用/停用。
- **FR-002**: 启用后，系统必须将来自 Claude Code 兼容入口的请求正确转接至 Packycode，并以 Claude Code 兼容格式将结果返回给调用方（对用户无感）。
- **FR-003**: 启用 Packycode 前，系统必须要求并验证有效的上游凭证；凭证缺失或无效时，禁止启用并提供可执行的修复指引。
- **FR-004**: 系统必须提供清晰的配置指南，覆盖必填项、默认值（隐私默认不存储请求/响应内容）与自检步骤。
- **FR-005**: 在默认隐私设置下，系统不得在代理侧持久化用户请求与响应内容；日志仅记录必要元信息用于排障，不含用户内容。
- **FR-006**: 当上游不可用或超时，系统必须保持稳定并输出明确的错误给调用方，同时允许快速停用 Packycode 以恢复到既有可用提供商（若已配置）。
- **FR-007**: 系统必须记录关键事件（启用、停用、配置变更、错误）以便审计与排障，且不泄露用户内容。
- **FR-008**: 系统必须至少支持当前 CLIProxyAPI 已实现的代码助理基础能力的无损转接（如标准请求/响应与取消）。
- **FR-009**: 系统必须在文档中明确说明：该集成主要面向 Codex CLI 场景优化，其他调用方式的适配范围与限制。
- **FR-010**: 系统必须在 `config.yaml` 中提供独立的 `packycode` 字段，用于专门配置 Packycode 转接；该字段需覆盖 Packycode 所需的全部关键选项（如 `base-url`、`requires-openai-auth`、`wire-api`、隐私与默认模型相关项），且不依赖 `codex-api-key` 字段。
- **FR-011**: 系统必须提供清晰的映射指引，说明如何从 Codex CLI 的 Packycode 配置（`~/.codex/config.toml` 与 `~/.codex/auth.json`）迁移/对应至 `config.yaml` 的 `packycode` 字段。
- **FR-012**: 当同时检测到 Codex CLI 的 Packycode 配置与 `config.yaml` 的 `packycode` 字段时，系统必须采用确定性的优先级（默认：`config.yaml` > 环境变量 > Codex CLI 配置），并在状态信息中可见地展示当前生效来源。
- **FR-013**: 系统必须提供 CLI 标志（如 `--packycode`）以在启用 Packycode 且配置校验通过时，主动将 OpenAI/GPT 模型（`gpt-5`、`gpt-5-*`、`gpt-5-codex-*`、`codex-mini-latest` 等）注册至全局模型注册表（provider=`codex`），从而确保 `/v1/models` 可见并可用。
- **FR-014**: 系统必须在服务启动与配置热重载回调中，针对已启用且配置合法的 Packycode 执行兜底模型注册（与 FR-013 同步的注册逻辑，幂等，不依赖文件变更事件）。

### Key Entities *(include if feature involves data)*

- **提供商（Provider）**：代表上游 AI 能力来源（如 Packycode）；包含名称、启用状态、兼容范围与限制说明。
- **凭证（Credential）**：代表接入上游所需的密钥/令牌等；包含有效性、过期状态与校验结果（不含明文存储要求）。
- **隐私策略（Privacy Setting）**：代表是否在代理侧保留用户内容的策略；默认不保留。
- **路由规则（Routing Rule）**：代表请求到上游的转接策略与选择逻辑（面向业务层面的“何时走何种上游”描述，而非技术细节）。
- **Packycode 配置块（Packycode Config Block）**：在 `config.yaml` 中的独立字段，承载 Packycode 所需关键选项（如 `base-url`、`requires-openai-auth`、`wire-api`、隐私与默认模型相关项）以及其有效性与来源（本地配置/环境变量/Codex CLI 配置）。

## Success Criteria *(mandatory)*

<!--
  ACTION REQUIRED: Define measurable success criteria.
  These must be technology-agnostic and measurable.
-->

### Measurable Outcomes

- **SC-001**: 首次集成者可在 ≤10 分钟内完成 Packycode 启用并成功发起一次标准请求（≥80% 一次通过）。
- **SC-002**: 95% 的请求在 2 秒内开始呈现可见结果，用户感知流畅，无明显卡顿。
- **SC-003**: 启用后首周内，与配置相关的请求失败率 <2%（占所有请求比例）。
- **SC-004**: 默认隐私设置下，代理侧 100% 不持久化用户请求与响应内容（通过抽查与日志策略验证）。
- **SC-005**: 上游不可用时，系统保持稳定并输出明确错误，平均恢复（停用或切回）操作 ≤1 分钟。
- **SC-006**: `config.yaml` 的 `packycode` 字段覆盖 Packycode 所需关键选项的可用性达到 100%，并通过样例配置与验收场景验证。
