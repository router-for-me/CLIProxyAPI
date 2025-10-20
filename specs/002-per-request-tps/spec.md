# Feature Specification: 每请求 TPS（Tokens Per Second）

**Feature Branch**: `002-per-request-tps`
**Created**: 2025-10-20
**Status**: Draft
**Input**: User description: "tps-for-request 我需要计算每一个请求的tps(tokens per second)"

## Clarifications

### Session 2025-10-20
- Q: TPS 字段在“响应元数据”中的呈现形态？ → A: 仅在日志中记录，不对外暴露
- Q: 日志中的 TPS 字段命名与分层？ → A: 扁平键：tps_completion, tps_total
- Q: TPS 计算误差与舍入规则是否固定为“两位小数、四舍五入”？ → A: 固定两位小数、四舍五入

### Implementation Notes (post-implementation)
- 新增独立开关 `tps-log`，仅控制 TPS 事件输出；`request-log` 仍控制请求级日志。
- 复用项目全局日志模块（LogFormatter 与输出通道）；不改变 `debug: true` 的原有样式与去向。
- TPS 事件名为 `per-request-tps`，仅写日志，不对外响应暴露；字段与口径与规范一致。
- Gin 上下文注入 `config` 指针以便门控读取；管理端公开 `/v0/management/tps-log`（GET/PUT/PATCH）。

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 在结构化日志中查看每请求 TPS (Priority: P1)

作为集成者/开发者，我可以在结构化日志中看到该请求的 TPS（至少包含“completion TPS”，可选“total TPS”），用于评估性能与排障。

**Why this priority**: 直接面向核心使用者，能最快反馈模型与上游通道的真实吞吐表现，支撑性能回归检测与SLO管理。

**Independent Test**: 触发成功请求后，检查结构化日志存在 `TPS` 数值字段，且值与手工计算在误差范围内一致。

**Acceptance Scenarios**:

1. Given 成功的非流式请求，When 输出 120 个 tokens，总请求耗时为 3.00 秒，Then 日志中的 completion TPS ≈ 40.00（允许±0.10 的误差，四舍五入保留两位小数）。
2. Given 成功的流式请求，When 从首个输出 token 到最后一个输出 token 的时间间隔为 2.50 秒，且输出 250 个 tokens，Then 日志中的 completion TPS ≈ 100.00（允许±0.10 的误差，保留两位小数）。
3. Given 请求成功但输出 tokens = 0，When 请求完成，Then 日志中记录 completion TPS = 0.00（字段存在）。
4. Given 请求失败或未产生任何输出 token，Then 日志侧不强制记录 TPS 字段（可记录故障原因与上下文）。

---

### User Story 2 - 在结构化日志中记录 TPS (Priority: P2)

作为观测/运维人员，我可以在结构化日志中获取每个请求的 TPS 相关字段，并据此在外部系统里进行筛选、聚合与对比。

**Why this priority**: 支撑离线分析与趋势监控，便于快速定位“变慢/限速/拥塞”等问题来源。

**Independent Test**: 触发请求后查看日志：包含 `tps_completion`（必有）与 `tps_total`（如可用）以及相应的时间窗与 tokens 计数。

**Acceptance Scenarios**:

1. Given 成功请求，Then 日志条目包含 completion TPS、tokens 计数与时间窗信息，数值为两位小数。
2. Given 流式请求，Then 日志条目包含流式时间窗（首/末 token 间隔）与 completion TPS。
3. Given 失败请求，Then 若产生过 ≥1 个输出 token，则日志包含已产生 tokens 与计算所得 TPS；否则不包含 TPS 字段。

---

### User Story 3 - 以 TPS 为条件进行外部分析/告警 (Priority: P3)

作为运维/成本分析人员，我可以基于 TPS 在外部平台设置阈值、做分段统计和回归对比，以便及时发现性能波动或上游限速影响。

**Why this priority**: 形成闭环的性能观测能力，降低 MTTR，提升容量规划质量。

**Independent Test**: 将样例日志导入分析工具，能够按 TPS 过滤/排序并生成分布图与阈值告警。

**Acceptance Scenarios**:

1. Given 含有 `tps.completion` 字段的样例日志，When 在外部分析工具中设置过滤条件 `tps.completion < 20`，Then 查询结果仅包含满足条件的记录。
2. Given 连续时间窗口的样例日志，When 设定阈值告警 `tps.total < 10`，Then 触发告警并记录告警事件。
3. Given 导入两批不同时间段的样例日志，When 生成 TPS 分布对比图，Then 能看出显著的分布差异并支持导出报告。

---

### Edge Cases

- 极短耗时导致 TPS 数值极大：按实测时间计算，统一四舍五入两位小数。
- 流式中途中断（已产生部分 tokens）：以已产生 tokens 与首/末 token 时间计算。
- 多次重试：TPS 按单次请求/尝试分别计算，不跨尝试汇总。
- 时间源与时钟偏差：开始/结束均使用同一时间源采集，确保自洽。
- 输入 tokens 很多但无输出：completion TPS = 0.00；total TPS 仍可按定义计算（如需）。
- 数值稳定性：避免除 0 或负值，遵循统一的舍入与缺失字段处理规则。

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 系统必须为每个请求计算 TPS（至少包含 completion TPS）。
- **FR-002**: 定义时间窗口：
  - 非流式：请求开始 → 请求完成（用于 `total TPS`）。
  - 流式：首个输出 token 时间 → 最后一个输出 token 时间（用于 `completion TPS`）。
- **FR-003**: 口径定义：
  - completion TPS = 输出 tokens 数 ÷ 输出时间窗（秒）。
  - total TPS = （输入 + 输出 tokens 数）÷ 请求时间窗（秒）。
- **FR-004**: 响应中不暴露 TPS 字段；结构化日志记录 completion TPS（必有），total TPS（如可用）；数值统一四舍五入保留两位小数，单位为 tokens/s。
- **FR-004.1**: 日志字段命名采用扁平键：`tps_completion`, `tps_total`。
- **FR-005**: 结构化日志必须记录：请求标识、时间窗口、tokens 计数、completion TPS、total TPS（如可用）及计算时间戳。
- **FR-006**: 失败/中断处理：若产生过 ≥1 个输出 token，则计算并记录相应 TPS；否则可省略 TPS 字段。
- **FR-007**: 不得记录个人敏感信息；仅暴露与度量相关的聚合数值。
- **FR-008**: TPS 采集对请求延迟的额外开销应可忽略（目标额外开销 ≤ 1% 或 ≤ 5ms，取更严格者）。
- **FR-009**: 数值稳定性：避免除 0/负值，使用统一时间源，舍入规则一致；字段名与语义在同一版本内保持稳定。
- **FR-010**: 对外文档或变更说明中明确字段含义与口径，以便外部分析工具映射。

### Key Entities *(include if feature involves data)*

- **PerRequestMetrics**：描述单次请求的观测数据（语义层面）：
  - `request_id`、`is_streaming`
  - `request_duration_seconds`、`stream_duration_seconds`（可选）
  - `input_tokens`、`output_tokens`、`total_tokens`
  - `tps_completion`、`tps_total`（可选）
  - `measured_at`（度量时间点）

### Acceptance Criteria Mapping (FR → Verification)

- FR-001：结构化日志中包含 `tps_completion` 字段；数值与手工计算相差 ≤ 5%。
- FR-002：对非流式/流式样例，时间窗分别按定义被正确识别，计算结果合理。
- FR-003：基于样例 tokens 与时间窗，`tps_completion`/`tps_total` 公式计算与记录一致。
- FR-004：日志中 `tps_completion` 保留两位小数；`tps_total` 在可用时存在且同样保留两位小数。
- FR-004.1：日志字段名为扁平键，解析成功且一致。
- FR-005：结构化日志含请求标识、时间窗、tokens 计数与 TPS 值，字段齐全且可解析。
- FR-006：失败但产生部分输出的样例，日志含已产生 tokens 与对应 TPS；零输出则不含 TPS 字段。
- FR-007：抽样检查日志，不出现个人敏感信息。
- FR-008：对照基线压测，TPS 采集引入的延时增量 ≤ 1% 或 ≤ 5ms。
- FR-009：异常/边界样例不出现除 0/负值；舍入一致；时间源一致。
- FR-010：对外说明中字段与口径与实际实现一致，外部分析工具可正确读取。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: ≥ 95% 的成功请求在结构化日志中包含 completion TPS，且格式一致、可读。
- **SC-002**: 在受控测试中，TPS 计算相对误差 ≤ 5%（以回放或人工基准为参照）。
- **SC-003**: 结构化日志覆盖率 100%，包含 TPS 相关字段（按规则出/缺省）。
- **SC-004**: 引入后，定位性能变慢/限速问题的平均时间（MTTR）较基线缩短 ≥ 30%。
- **SC-005**: 指标采集带来的额外延迟 ≤ 1% 或 ≤ 5ms（取更严格者）。

## Assumptions & Dependencies

### Assumptions
- 输出 tokens 计数与输入 tokens 计数可从同一请求上下文准确获得。
- 流式请求能准确标记“首个输出 token 时间”和“最后一个输出 token 时间”。
- 单一时间源（同一时钟域）用于所有时间戳，避免时钟漂移造成误差。
- 四舍五入统一为两位小数；当分母为 0 或不可用时，TPS = 0.00 或字段缺省，且不抛错。

### Dependencies
- 系统具备结构化日志能力，允许新增 TPS 相关字段。
- 无需扩展响应元数据（对外返回结构保持不变）。
- 外部分析/告警平台的接入与配置不在本功能范围（由使用方完成）。
