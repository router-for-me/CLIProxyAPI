<!--
Sync Impact Report
- Version change: N/A → 1.0.0
- Modified principles: (initial adoption)
- Added sections: Core Principles (I–V), Additional Constraints, Development Workflow, Governance
- Removed sections: None
- Templates requiring updates:
  - ✅ .specify/templates/spec-template.md — 已与原则对齐（技术无关、可度量、验收场景）
  - ✅ .specify/templates/plan-template.md — 已包含 Constitution Check 门槛
  - ✅ .specify/templates/tasks-template.md — 任务分期与测试分类与原则一致
  - ⚠  .specify/templates/commands/*.md — 未检测到该目录，无需更新
- Follow-up TODOs: 如需自动化 Gate 校验，可于 CI 中新增检查脚本（后续迭代）
-->

# CLIProxyAPI Constitution

## Core Principles

### I. 规格优先（用户价值与技术无关）
特性规格必须聚焦“做什么/为何做”，避免实现细节泄漏：
- MUST 明确用户旅程、验收场景（Given-When-Then）与边界情况。
- MUST 给出可量化、可验证的成功标准（技术无关的业务/体验指标）。
- MUST 列出可测试的功能性需求与清晰范围/假设；避免含糊表达。
- SHOULD 在未指定细节时给出合理默认，并记录在“Assumptions”。
Rationale：确保跨团队沟通清晰、可测可验，减少返工。

### II. 契约与测试驱动
在实现前或并行生成契约与测试：
- MUST 为对外接口/管理接口生成契约（如 OpenAPI）并随变更更新。
- MUST 以验收场景为依据编写合约/集成测试；必要处补充单元测试。
- SHOULD 对跨组件交互与共享 Schema 编写集成测试。
Rationale：降低集成失败风险，保障对外/对内接口稳定性。

### III. 隐私优先与最小化留存
- MUST 默认不持久化用户请求/响应内容；日志仅记录必要元信息。
- MUST 对密钥/凭证进行安全处理；配置来源可追溯且有优先级策略。
- SHOULD 在文档中清晰披露隐私与留存策略及其默认值。
Rationale：保护用户数据与合规，减少敏感信息暴露面。

### IV. 简单稳定与向后兼容
- MUST 遵循 YAGNI，避免无谓复杂度与过早抽象。
- MUST 为破坏性变更提供迁移指引；能降级/回退保持可用。
- SHOULD 采用功能开关/配置开关，以渐进发布与快速止血。
Rationale：降低维护成本，提升可用性与演进弹性。

### V. 可观测与可运维
- MUST 使用结构化日志与关键事件审计（不含用户内容）。
- MUST 暴露核心运行与质量指标，反映成功标准达成度。
- SHOULD 在状态/诊断信息中展示“配置生效来源”。
Rationale：便于故障定位、回归分析与运维决策。

## Additional Constraints
- 文档语言以项目文档为准；规格与计划面向非技术干系人撰写，避免栈/框架细节。
- 规格模板中的强制章节（用户场景、需求、成功标准）不得省略；不适用时应删除该段落而非保留“空/N/A”。
- 优先级策略（示例）：`config.yaml` > 环境变量 > 外部工具配置（如 Codex CLI）。

## Development Workflow
- 规格 → 研究 → 计划 → 设计与契约 → 实现/测试 → 运维文档。
- 使用 speckit 命令族生成 spec/plan/research/contracts/data-model/quickstart，并在变更时保持同步。
- “Constitution Check” 作为计划关卡：若违反核心原则，须在“Complexity Tracking”中给出明确理由与替代方案评估。

## Governance
- 宪法优先级高于其它实践；任何偏离必须在 PR 中说明并获得批准。
- 版本号遵循语义化：
  - MAJOR：删除/重定义原则或不兼容治理变化。
  - MINOR：新增原则/章节，或显著扩展指导。
  - PATCH：表述澄清/错别字/非语义修订。
- 修订流程：提出变更 → 影响评估与模板同步 → 评审通过 → 合入并更新版本/日期。

**Version**: 1.0.0 | **Ratified**: 2025-10-20 | **Last Amended**: 2025-10-20
