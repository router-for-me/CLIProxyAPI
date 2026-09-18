---
# —— spec-dev 漂移守卫锚点（机器可校验，勿删）——
spec_dev:
  version: 1
  feature: upstream-response-model
  status: active
  covers:
    - "sdk/cliproxy/usage/**"
    - "internal/runtime/executor/helps/**"
    - "sdk/cliproxy/auth/conductor_execution.go"
    - "sdk/cliproxy/auth/conductor_stream.go"
    - "sdk/cliproxy/auth/conductor_home_execution.go"
    - "internal/redisqueue/**"
  sync_commit: null
  supersedes: []
  superseded_by: null
---

# 上游响应模型 设计

## 背景与目标

CLIProxyAPI 的用量记录只有发往上游的 `model` 和客户端 `alias`。force-mapping 会在成功返回前改写响应里的模型名，下游和 Keeper 都看不到上游自己声明的模型。本特性在改写之前从上游响应原文读出该名称，随用量队列送给 cpa-usage-keeper，并在请求事件模型列于不一致时显示。

**成功标准 / Success criteria**：一次成功或带响应体的失败请求，只要原文声明了模型，用量记录与 Keeper 事件都能读到该值；仅当它与发往上游的 `model` 不一致时，Usage「请求事件」和凭证详情请求表的模型列出现第三行「上游响应: {name}」；CSV/JSON 导出始终包含该列。下游客户端响应与今天一致。

## 非目标

- 不按上游响应模型计费，不改价格或筛选/分析聚合维度
- 不在模型列加「模型不一致 / 疑似版本变体」徽章
- 不改 plugin API（`pluginapi.UsageRecord`）
- 不保证不经 `AppendAPIResponseChunk` / WebSocket 原文入口的 plugin executor 路径
- 不改 TUI、request-log 结构化字段、CLIProxyAPIHome
- 不落库 mismatch/conflict 布尔值，不做 grok 构建号别名特例
- 不改写下游响应里的模型字段（force-mapping 行为保持不变）

## 术语表

- **发往上游模型 / sent-upstream model**：用量记录的 `model`，即路由/别名解析后真正发给上游的名称。_Avoid_：请求模型（易与 alias 混淆）。
- **客户端别名 / client alias**：用量记录的 `alias`，即客户端请求名。_Avoid_：上游模型。
- **上游响应模型 / upstream response model**：上游响应原文在 translator / force-mapping 之前自报的模型名。_Avoid_：响应体、原始响应、response_model（易与计费源混淆）。
- **不一致 / mismatch**：上游响应模型非空，且与发往上游模型做 trim 后大小写不敏感比较不相等。_Avoid_：conflict（同一次响应内多声明打架，本轮不落库）。

## 参与者与适用行为

- **用量发布路径（conductor + executor）**：每次上游尝试创建观察器；在原文入口观测；发布 `usage.Record` 时带上观测值。适用 Scenario：「非流式原文观测」「流式终态覆盖」「失败体仍观测」「重试不串味」。
- **Redis 用量队列**：把非空 `upstream_response_model` 写入入队 JSON。适用 Scenario：「队列写出观测值」。
- **cpa-usage-keeper 入库与 API**：解码、落热表/归档、列表与导出带出该字段；旧事件缺字段视为空。适用 Scenario：「Keeper 持久化」「导出始终带列」「缺省降级」。
- **Keeper 使用者（Usage 请求事件、凭证详情请求表）**：只在 mismatch 时看到第三行。适用 Scenario：「不一致才显示第三行」「一致或空值不显示」。
- **下游 API 客户端**：收不到本特性新增字段，响应模型改写与今天相同。无新增错误路径。
- **Home 401 发布器**：无上游响应体，字段保持空。适用 Scenario：「无响应体保持空」。

不把后台身份推成已有凭据策略。Plugin 用量适配器本轮不是参与者。

## 影响面

本仓库：`sdk/cliproxy/usage`（Record 字段与观察器）、`internal/runtime/executor/helps`（字节解析与 Append/Emit 入口）、`sdk/cliproxy/auth` 的 `newUpstreamAttemptContext`（每尝试 Begin）、`internal/redisqueue`（入队 JSON）。不改 translator、不改 force-mapping 重写器。

耦合仓库 **cpa-usage-keeper**（不在本仓库 `covers` 内，但本特性必须一起交付）：`queuedUsageDetail` 解码、`UsageEvent` / 归档列与迁移、列表投影与 DTO、`/usage/events` 与导出、`RequestEventsDetailsCard` 与 `CredentialRequestEventsList`、i18n。概览/分析 rollup 维度不增加该字段。

无新第三方依赖。

## 已确认的关键决策

- 显示触发：仅 mismatch 时显示第三行 —— 对齐 sub2api 与现有 alias「相同则藏」。
- 观测覆盖：全协议 HTTP（流式/非流）+ WebSocket —— 用户选定，避免只覆盖部分渠道。
- 展示面：Usage 请求事件 + 凭证详情请求表 + CSV/JSON 导出 —— 用户选定扩展。
- 不一致标法：只要第三行「上游响应: xxx」，不加徽章 —— 用户选定。
- 实现方案：原文入口统一观测，再沿用量队列进 Keeper（方案 A）—— 漏接面小于逐 executor 接线，也避免把模型审计塞进 token 解析器。
- JSON/字段名：`upstream_response_model` —— 对齐 sub2api 审计字段，不与 `model` 撞车。
- mismatch 在展示层计算，不落库布尔字段 —— 本轮无计费，避免重复状态。
- 本轮不扩 plugin API、筛选、分析聚合、TUI、request-log 结构化字段、计费、grok 别名、conflict 落库。

未建 ADR：观测入口可随实现调整，不满足「难以逆转」判据。

## 约束归属与拒绝的解读

| 约束 | 负责边界 | 验证位置 |
|------|----------|----------|
| 观测不得改转发字节 | helps 观测入口；不调用 rewriter | Scenario「观测不改写出站字节」 |
| 必须在 force-mapping 之前读原文 | Append/Emit 位于 translator/rewrite 之前 | Scenario「非流式原文观测」 |
| 观测不依赖 RequestLog / CommercialMode | Observe 在日志门控之前 | Scenario「关闭 request-log 仍观测」 |
| mismatch 只比发往上游模型，不比 alias | Keeper 展示层 | Scenario「不一致才显示第三行」 |
| 空值降级，不得阻断入队或渲染 | 发布、解码、UI | Scenario「缺省降级」「无响应体保持空」 |

拒绝的解读：

- 原表达「模型实际上游响应」——排除「把 `Record.Model` 当成上游自报」——来源：CPA `usage.Record` 字段语义与用户确认的 sub2api 三字段模型——采用：新字段 `upstream_response_model`。
- 原表达「模型列增加这个显示」——排除「新开一列或始终显示第三行」——来源：用户选择 mismatch + 第三行——采用：现有模型列内堆叠第三行。
- 「凭证页」——排除「CLIProxyAPI 本体页面」——来源：用户追问后确认——采用：cpa-usage-keeper 凭证详情请求表。

## 取代与共存

无相交 active spec。`.spec-dev/` 此前不存在。独立审查曾建议拆成 CPA / Keeper 两份 roadmap 子项目；不采纳——成功标准是端到端第三行，队列键名必须由同一份行为规范锁死。实施计划用两个仓库的顺序任务，不是两个特性。

## 行为规范（Requirements）

### Requirement: Observe the model declared in raw upstream bytes

系统 SHALL 从 translator / force-mapping 之前的上游响应原文中读取上游响应模型。

#### Scenario: 非流式原文含 model

- **GIVEN** 上游非流式 JSON 在 `model` 或 `response.model` 声明 `gpt-5.4`，且 force-mapping 会把出站模型改成客户端别名
- **WHEN** 该次尝试发布用量记录
- **THEN** 记录的上游响应模型是 `gpt-5.4`，不是别名

#### Scenario: 无声明则保持空

- **GIVEN** 上游响应原文没有可解析的模型字段
- **WHEN** 发布用量记录
- **THEN** 上游响应模型为空

### Requirement: Observation does not alter forwarded responses

观测 SHALL 不修改写给下游客户端的响应字节。

#### Scenario: 观测不改写出站字节

- **GIVEN** 上游原文声明模型 `upstream-model`
- **WHEN** 系统完成转发（无论是否开启 force-mapping）
- **THEN** 出站 payload 与未做本特性时相同

### Requirement: Isolate observation per upstream attempt

每次上游尝试 SHALL 使用新的观察器，失败重试不得沿用上一次尝试的观测值。

#### Scenario: 重试不串味

- **GIVEN** 第一次尝试的原文声明 `model-a` 且失败，第二次尝试声明 `model-b` 且成功
- **WHEN** 成功尝试发布用量记录
- **THEN** 上游响应模型是 `model-b`

### Requirement: Extract models with protocol-specific rules

系统 SHALL 按**一次尝试一个抽取族**从原文抽取模型，不得对同一段 payload 同时套用两套 terminal 规则。

抽取族在该尝试的 `UsageReporter` 创建时绑定，映射为：provider/executor 标识属于 Claude 族则用 Claude 规则；属于 Gemini、Antigravity、AI Studio、Vertex 则用 Gemini 规则；其余（含 OpenAI 兼容、Codex、Kimi、xAI、Devin、Interactions）用 OpenAI 规则。绑定前若已有原文到达，按字段嗅探一次：存在 `modelVersion` / `response.modelVersion` / `response.response.modelVersion` 用 Gemini；存在 `message.model` 用 Claude；否则用 OpenAI。嗅探只选一族。

各族规则：

- OpenAI：路径 `response.model` 然后 `model`。terminal 事件名为 `response.completed`、`response.done`、`response.failed`、`response.incomplete`、`response.cancelled`、`response.canceled`。
- Claude：路径 `message.model` 然后 `model`。声明一律非 terminal（只保留 first）。
- Gemini：路径 `modelVersion`、`response.modelVersion`、`response.response.modelVersion`。每条声明视为 terminal（后写覆盖）。

terminal 覆盖已有选中值；非 terminal 只在尚无 first 时写入。空白丢弃。名称 trim 后按 rune 截到 200。发现候选后若整段 JSON 非法，该次声明作废。

观测以入口收到的**一段字节**为单位，不把相邻 chunk 拼成完整帧。生产入口常把 SSE `event:` 与 `data:` 分成两次调用。观察器 SHALL 记住本次尝试最近一次 `event:` 行的事件名；随后的 `data:` 行用该事件名判定 OpenAI terminal。同一段字节里的 JSON `type` 也当作事件名。仅 `event:`、没有 data 的行不抽模型。

#### Scenario: OpenAI 流式终态覆盖

- **GIVEN** 同一尝试先出现非终态 `model` 为 `first-model`，再出现 `response.completed` 且 `response.model` 为 `final-model`
- **WHEN** 发布用量记录
- **THEN** 上游响应模型是 `final-model`

#### Scenario: 非法 JSON 丢弃声明

- **GIVEN** 一段字节看起来含 `"model"` 字段但整段不是合法 JSON
- **WHEN** 观察该段字节
- **THEN** 该声明不写入观察器

#### Scenario: 超长名称截断

- **GIVEN** 上游声明的模型名 trim 后超过 200 个 rune
- **WHEN** 发布用量记录
- **THEN** 上游响应模型等于截断到 200 rune 后的值

#### Scenario: Claude 只保留 first

- **GIVEN** 抽取族为 Claude，先声明 `message.model` 为 `claude-first`，再声明 `claude-later`
- **WHEN** 发布用量记录
- **THEN** 上游响应模型是 `claude-first`

#### Scenario: Gemini 后写覆盖

- **GIVEN** 抽取族为 Gemini，先声明 `modelVersion` 为 `gemini-a`，再声明 `gemini-b`
- **WHEN** 发布用量记录
- **THEN** 上游响应模型是 `gemini-b`

#### Scenario: 分行 SSE 终态

- **GIVEN** 抽取族为 OpenAI，同一尝试先观测到字节 `event: response.completed`，再观测到 `data: {"response":{"model":"final-model"}}`
- **WHEN** 发布用量记录
- **THEN** 上游响应模型是 `final-model`

### Requirement: Publish the observed model on usage records

`usage.Record` SHALL 携带字段 `UpstreamResponseModel`，值为该次发布对应尝试的观察结果，失败发布在已观测到值时同样携带。

#### Scenario: 失败体仍观测

- **GIVEN** 上游返回非 2xx，响应体仍声明模型 `err-model`
- **WHEN** 系统发布失败用量记录
- **THEN** 该记录的上游响应模型是 `err-model`

#### Scenario: 无响应体保持空

- **GIVEN** Home 401 等路径没有上游响应体
- **WHEN** 发布用量记录
- **THEN** 上游响应模型为空

### Requirement: Emit the observed model on the usage queue

Redis 用量入队 JSON SHALL 在观测值非空时包含 `upstream_response_model`；为空时省略该键。解码方把缺键与空字符串都视为空。

#### Scenario: 队列写出观测值

- **GIVEN** 用量记录的上游响应模型是 `claude-sonnet-4-6`
- **WHEN** redis 队列插件处理该记录
- **THEN** 入队 JSON 的 `upstream_response_model` 为 `claude-sonnet-4-6`

### Requirement: Observe independently of request logging

只要原文到达观测入口，系统 SHALL 进行观测，即使 request-log 关闭或 CommercialMode 开启（二者都会关掉 request-log 捕获，不得一并关掉观测）。

#### Scenario: 关闭 request-log 仍观测

- **GIVEN** request-log 未启用，或 CommercialMode 开启
- **WHEN** 上游原文到达 `AppendAPIResponseChunk` 或 WebSocket 原文入口
- **THEN** 随后发布的用量记录仍带上能解析出的上游响应模型

### Requirement: Persist the observed model on request events

cpa-usage-keeper SHALL 把队列中的 `upstream_response_model` 写入请求事件（热表与归档），缺省为空字符串；列表 API 返回该字段。

#### Scenario: Keeper 持久化

- **GIVEN** 入队 JSON 含 `upstream_response_model` 为 `gpt-5.4`
- **WHEN** Keeper 解码并插入事件后查询列表
- **THEN** 该事件的 `upstream_response_model` 为 `gpt-5.4`

### Requirement: Show the upstream response model only on mismatch

Usage「请求事件」模型列与凭证详情请求表的模型列 SHALL 仅在 mismatch 时增加一行 `{t('usage_stats.upstream_response_model')}: {name}`。文案键为 `usage_stats.upstream_response_model`，英文 `Upstream response`，简体「上游响应」，繁体「上游響應」。一致或空值时不出现该行，也不出现「Upstream response: -」或「上游响应: -」。比较基准是发往上游模型，不是客户端别名。不加徽章。

#### Scenario: 不一致才显示第三行

- **GIVEN** 事件 `model` 为 `mapped-model`，`model_alias` 为 `client-alias`，`upstream_response_model` 为 `actual-model`
- **WHEN** 渲染 Usage 请求事件或凭证详情请求表的模型列
- **THEN** 可见第三行文案包含「上游响应」与 `actual-model`，且没有不一致徽章

#### Scenario: 一致或空值不显示

- **GIVEN** 事件的上游响应模型为空，或与 `model` 仅大小写不同
- **WHEN** 渲染上述模型列
- **THEN** 不出现「上游响应」行

### Requirement: Include the observed model in event exports

事件 CSV 与 JSON 导出 SHALL 始终包含 `upstream_response_model` 列或字段；无值时为空。

#### Scenario: 导出始终带列

- **GIVEN** 两条事件，一条有上游响应模型，一条没有
- **WHEN** 导出 CSV 或 JSON
- **THEN** CSV 表头与 JSON 对象均含 `upstream_response_model`，两条记录分别写出该值与空

### Requirement: Degrade when the observed model is absent

缺少上游响应模型时，系统 SHALL 照常完成用量入队、Keeper 入库与页面渲染，不把缺字段当成错误。

#### Scenario: 缺省降级

- **GIVEN** 旧版 CPA 入队 JSON 没有 `upstream_response_model` 键
- **WHEN** Keeper 解码、入库并打开请求事件页
- **THEN** 事件存空字符串，模型列不出现「上游响应」行，导出该列为空

## 方案设计

### 架构与组件

- **Observer**（`sdk/cliproxy/usage`）：一次尝试一个实例。`Observe(model, terminal)`、`Model()`。不解析协议。
- **字节解析**（`internal/runtime/executor/helps`）：`ObserveUpstreamResponseBytes(ctx, payload)` 按已绑定抽取族抽模型。`event:` 行只更新本次尝试记住的事件名；`data:` 行用该事件名（或 JSON `type`）判定 OpenAI terminal。
- **尝试生命周期**：`newUpstreamAttemptContext` 调用 `BeginUpstreamResponseModelObservation`。`NewUsageReporter` 按 provider/executor 绑定抽取族。发布时从 ctx 抄到 `Record.UpstreamResponseModel`。
- **观测入口**：`AppendAPIResponseChunk`、`AppendAPIWebsocketResponse`、`EmitWebSocketResponseEvent` 在 RequestLog/CommercialMode 门控之前调用解析。
- **Redis 插件**：入队字段 `upstream_response_model`。
- **Keeper**：解码 → 实体/迁移 → 投影/DTO/API → 两处表格与导出。

本特性是**一份实施计划、两个仓库的顺序任务**：先合 CLIProxyAPI 入队字段，再合 cpa-usage-keeper 展示。不拆成两份 spec / roadmap 子项目——成功标准是端到端第三行，契约键名必须由同一份行为规范锁死。

### 数据流

```
上游原文 → Observe（不改字节）→ translator / force-mapping
        → UsageReporter.Publish → Record.UpstreamResponseModel
        → redis JSON → Keeper 入库 → 列表/导出
        → UI：mismatch 才显示第三行
```

### 关键接口

```
func BeginUpstreamResponseModelObservation(ctx context.Context) context.Context
func ObserveUpstreamResponseBytes(ctx context.Context, payload []byte)
func UpstreamResponseModelFromContext(ctx context.Context) string

usage.Record.UpstreamResponseModel string
queuedUsageDetail.UpstreamResponseModel string `json:"upstream_response_model,omitempty"`
```

Keeper 存储列 `upstream_response_model TEXT NOT NULL DEFAULT ''`。列表/导出 JSON 键 `upstream_response_model`。前端 `UsageEvent.upstream_response_model?: string`。i18n 键 `usage_stats.upstream_response_model`。无新 HTTP 路由。

### 错误处理

- 无声明、非法 JSON、无 observer：字段空，不报错。
- 观测逻辑失败不得阻断转发或用量入队。
- 旧行/旧包缺字段视为空。
- Plugin executor 不经原文入口：本轮允许空字段。

## 测试与验收策略

### 测试落点声明

来源：阶段 5 获批设计。无新的可替换外部依赖。

| 公共落点 | 覆盖 Scenario | 允许替换的依赖 |
|----------|---------------|----------------|
| `sdk/cliproxy/usage` observer 公共方法 | 无声明则保持空、超长名称截断、重试不串味（配合 Begin） | 无 |
| `helps.ObserveUpstreamResponseBytes` | OpenAI 流式终态覆盖、非法 JSON 丢弃声明、Claude 只保留 first、Gemini 后写覆盖、分行 SSE 终态 | 无 |
| `AppendAPIResponseChunk` / WebSocket 原文入口 | 关闭 request-log 仍观测、观测不改写出站字节（入口不改 chunk） | 无 |
| `UsageReporter` Publish / PublishFailure | 非流式原文含 model、失败体仍观测、无响应体保持空 | 无 |
| `internal/redisqueue` 入队 JSON | 队列写出观测值 | 无 |
| Keeper `DecodeRedisUsageMessage` | Keeper 持久化、缺省降级 | 无 |
| Keeper 列表 API 与 CSV 导出 | 导出始终带列、Keeper 持久化 | 无 |
| `RequestEventsDetailsCard` 与 `CredentialRequestEventsList` | 不一致才显示第三行、一致或空值不显示 | 无 |

不逐 executor 增加私有测试。全协议覆盖由「凡走 Append/Emit 的路径都经过入口」保证。

| Scenario / 检查项 | 维度 | 执行方式 | 验收证据 |
|-------------------|------|---------|---------|
| 非流式原文含 model | unit | 任务内 TDD | 测试通过 |
| 无声明则保持空 | unit | 任务内 TDD | 测试通过 |
| 观测不改写出站字节 | unit | 任务内 TDD | 测试通过 |
| 重试不串味 | unit | 任务内 TDD | 测试通过 |
| OpenAI 流式终态覆盖 | unit | 任务内 TDD | 测试通过 |
| 非法 JSON 丢弃声明 | unit | 任务内 TDD | 测试通过 |
| 超长名称截断 | unit | 任务内 TDD | 测试通过 |
| Claude 只保留 first | unit | 任务内 TDD | 测试通过 |
| Gemini 后写覆盖 | unit | 任务内 TDD | 测试通过 |
| 分行 SSE 终态 | unit | 任务内 TDD | 测试通过 |
| 失败体仍观测 | unit | 任务内 TDD | 测试通过 |
| 无响应体保持空 | unit | 任务内 TDD | 测试通过 |
| 队列写出观测值 | unit | 任务内 TDD | 测试通过 |
| 关闭 request-log 仍观测 | unit | 任务内 TDD | 测试通过 |
| Keeper 持久化 | unit | 任务内 TDD | 测试通过 |
| 不一致才显示第三行 | unit | 任务内 TDD | 测试通过 |
| 一致或空值不显示 | unit | 任务内 TDD | 测试通过 |
| 导出始终带列 | unit | 任务内 TDD | 测试通过 |
| 缺省降级 | unit | 任务内 TDD | 测试通过 |
| CPA 入队后 Keeper 能展示 mismatch 行 | e2e | 验收任务 (D) | 手工或联调：一条映射不一致的请求在两处表格出现第三行 |

## 风险与边缘情况

- 半截 chunk 不拼接，可能漏掉只出现在残帧里的模型；终态完整帧通常仍带模型。
- Gemini 族「每条声明当 terminal」与 OpenAI first/terminal 不同，已写入抽取规则。
- WebSocket 多轮若现有用量只 Publish 一次，观测值是该次发布前的累积结果（terminal 赢）。
- 必须先交付 CPA 入队字段，Keeper 才能看到值；两仓各自 PR，契约键名固定为 `upstream_response_model`。
- `covers` 不含 keeper 路径；Keeper 变更不受本仓库漂移守卫拦截，计划里单独列任务。

## 开放问题

- observer 的具体类型是导出还是包内，由实施按现有 usage context helper 风格选择，只要 Begin / FromContext / Record 字段保持可测。
- Keeper 迁移版本号按该仓库当日序号选取，列定义以本 spec 为准。
