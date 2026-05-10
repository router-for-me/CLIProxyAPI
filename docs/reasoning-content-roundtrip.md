# Reasoning Content Round-Trip: 修复总结

## 背景

Codex CLI 使用 OpenAI Responses API (`/v1/responses`) 格式与 CLIProxyAPI 通信，代理将其转换为 Chat Completions 格式发送给上游提供商（如 DeepSeek）。Responses API 和 Chat Completions 对 reasoning/thinking 的处理方式不同，导致往返过程中 `reasoning_content` 丢失。

## 核心问题

### 问题 1: `reasoning_content` 在响应转换中被丢弃

**现象**: DeepSeek 返回 `reasoning_content`（思考内容），但代理在转换为 Responses 格式时丢弃了该字段。

**根因**: 旧代码中 `response.go` 有 DeepSeek 特判逻辑：

```go
// NOTE: reasoning_content is intentionally skipped for DeepSeek compatibility.
_ = delta
```

**修复**: 所有模型一视同仁——`reasoning_content` 转换为两个 Responses 格式的表达：

1. **`type: "reasoning"` output item** — 独立推理输出项，包含 summary text
2. **`type: "reasoning_text"` content part** — 嵌入 assistant message 的 content 数组，用于往返透传

### 问题 2: `reasoning_content` 在后续请求中未回传

**现象**: DeepSeek 要求同一对话中后续请求必须传回 `reasoning_content`，否则报错：`"The reasoning_content in the thinking mode must be passed back to the API"`

**根因**: 即使代理在响应中正确包含了 `reasoning_text`，Codex CLI（及大多数客户端）在构建后续请求时**不会**将该字段包含在 input 中。有两种情况：

#### 情况 A: 客户端未回传 `reasoning_text`
客户端发送的 input 中 assistant message 的 content 只有 `output_text`，没有 `reasoning_text`。代理的请求转换器无法从中提取 `reasoning_content`。

#### 情况 B: 客户端发来空 summary 的 `type: "reasoning"` 项
客户端发送 `{"type": "reasoning", "summary": [{"text": ""}]}`，summary text 为空，代理无法提取有效内容来注入。

**修复**: 请求方向增加 `case "reasoning":` 分支和 `pendingReasoningContent` 注入机制，同时保留 `case "reasoning_text":` 处理。

### 问题 3: DeepSeek 默认思考模式导致连锁反应

**现象**: 删除了 `thinking: {type: "disabled"}` 后，DeepSeek 进入默认思考模式，返回 `reasoning_content`，从而触发 echo-back 要求。

**根因**: `deepseek-v4-flash` 模型默认启用思考模式。旧代码无条件设置了 `thinking: {type: "disabled"}` 来规避此问题。新代码删除了这个逻辑后，DeepSeek 默认思考，产生 `reasoning_content`，进而要求回传。但客户端不回传 → 报错。

**修复**: 根据 `reasoning` 参数的有无/值来决定：

| 客户端传入 | 代理转换行为 |
|---|---|
| `reasoning: null` 或不存在 | `thinking: {type: "disabled"}` — 禁用思考模式 |
| `reasoning: {effort: "high"}` | `reasoning_effort: "high"` — 启用思考模式并设定强度 |

这样客户端不要求 reasoning 时，DeepSeek 不会进入思考模式，自然没有 echo-back 要求。

## 架构决策

### 通用 vs 厂商特化

**原则**: 代理应该是通用型的，不针对特定模型做特判。

- `reasoning_content` → `reasoning_text` 转换：**通用逻辑**，所有模型一视同仁
- `thinking: {type: "disabled"}`：**厂商兼容层**，因为只有 DeepSeek 需要此参数来禁用思考模式。其他模型忽略此参数

### 数据流

```
Request (Responses API)
  │
  ▼
request.go: ConvertOpenAIResponsesRequestToOpenAIChatCompletions
  ├── 处理 input 数组中的 message / function_call / reasoning 项
  ├── 从 reasoning_text content 中提取 → reasoning_content
  ├── 从 type:reasoning 项中提取 summary → pendingReasoningContent
  ├── pendingReasoningContent → assistant message.reasoning_content
  └── reasoning 参数映射 → thinking:disabled / reasoning_effort
  │
  ▼
Chat Completions Request → Upstream Provider
  │
  ▼
Chat Completions Response ← Upstream Provider
  │
  ▼
response.go: ConvertOpenAIChatCompletionsResponseToOpenAIResponses
  ├── reasoning_content → type:reasoning output item
  ├── reasoning_content → message content 中的 reasoning_text content part
  ├── 流式: 逐 chunk 发射 reasoning 事件
  └── 非流式: 聚合到 response.completed
  │
  ▼
Response (Responses API) → Client
```

## 修改的文件

### `internal/translator/openai/openai/responses/openai_openai-responses_request.go`

1. **tool_calls 分组缓冲**（此前已修复）:
   - 旧: `flushFunctionCalls()` — 遇 message 就 flush，不处理交错消息
   - 新: `flushToolGroup()` — 三缓冲（function_calls + bufferedMessages + tool_outputs），按 Chat Completions 严格顺序排放

2. **`case "reasoning_text":`** — 从 message content 中提取推理文本到 `reasoning_content` 顶层字段

3. **`case "reasoning":`** — 从 standalone reasoning input item 的 summary 中提取文本，注入下一个 assistant message

4. **`pendingReasoningContent`** — 在 buffered 和 direct 两个消息路径中注入到 assistant message

5. **`reasoning` 参数处理**:
   - `reasoning: {effort: "..."}` → `reasoning_effort: "..."`
   - `reasoning: null` 或不存在 → `thinking: {type: "disabled"}`

### `internal/translator/openai/openai/responses/openai_openai-responses_response.go`

1. **流式 reasoning 处理**:
   - 去掉 DeepSeek 特判的 `_ = delta`
   - `reasoning_content` delta → `response.reasoning_summary_part.added` + `response.reasoning_summary_text.delta`
   - content delta 前关闭 reasoning → 发射 done 事件
   - content_part.added 支持 `reasoning_text` (index 0) + `output_text` (index 1)

2. **`buildResponsesCompletedEvent`**:
   - message content 动态构建：有 reasoning 时先插 `reasoning_text`，再插 `output_text`

3. **非流式 `ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream`**:
   - 去掉 `_ = rawJSON / _ = requestRawJSON`
   - 检测 `choices.0.message.reasoning_content`，创建 reasoning output item + reasoning_text content part

## 验证清单

- [x] 非流式响应: message content 包含 `reasoning_text`
- [x] 流式响应: SSE 事件的 content 包含 `reasoning_text`
- [x] `reasoning: null`: 不返回 reasoning_content，无 echo-back 要求
- [x] `reasoning: {effort: "low"}`: 返回 reasoning_content + reasoning_text
- [x] 带 function_calls 的 follow-up 请求正常运行
- [x] 无 `go vet` / `go build` 错误

## 适配其他产品的要点

1. **request 方向**: 注意客户端是否在 input 中回传 `reasoning_text`。如果客户端不回传，必须通过 `thinking: disabled` 或其他机制防止提供商的思考模式被默认激活

2. **response 方向**: 推理内容需要同时以两种形式存在——独立 reasoning item（供 UI 展示）和 message content 中的 reasoning_text（供往返透传）

3. **厂商差异**: DeepSeek 需要显式 `thinking: {type: "disabled"}` 来禁用思考模式；其他模型可能用不同参数。建议在 provider 配置层抽象

4. **客户端行为**: Codex CLI 不会回传 `reasoning_text`，这是一个关键假设。如果客户端行为不同（如回传 `reasoning_text`），可以去掉 `thinking: disabled` 的兜底逻辑
