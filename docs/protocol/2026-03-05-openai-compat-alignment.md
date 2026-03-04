# 2026-03-05 OpenAI 兼容层协议对齐修复记录

## 背景
- 目标仓库：`CLIProxyAPI-main.__latest_20260304000229`
- 修复范围：`OpenAI <-> Claude/Gemini/Gemini-CLI` 的请求与响应转换
- 重点：参数兼容、字段类型一致性、流式/非流式行为一致性、工具调用字段保真

## 已修复问题

### 1) `tool_choice` 对象被错误字符串化
- 文件：`internal/translator/openai/openai/responses/openai_openai-responses_request.go`
- 修复：对象/数组使用 `SetRaw`，基础类型使用 `Set`
- 结果：保留 `tool_choice` 原始 JSON 结构

### 2) Gemini Responses 丢失 `function_call_output.output` 对象
- 文件：`internal/translator/gemini/openai/responses/gemini_openai-responses_request.go`
- 修复：读取 `output` 的 `.Raw/.Value()`，对象走 `SetRaw`
- 结果：工具输出对象不再丢字段

### 3) Gemini Responses 未映射 OpenAI `stop`
- 文件：`internal/translator/gemini/openai/responses/gemini_openai-responses_request.go`
- 修复：同时支持 `stop` 与 `stop_sequences`
- 结果：统一映射到 `generationConfig.stopSequences`

### 4) Gemini Chat 未映射 `max_tokens` / `max_completion_tokens` / `stop`
- 文件：`internal/translator/gemini/openai/chat-completions/gemini_openai_request.go`
- 修复：
  - `max_tokens|max_completion_tokens -> generationConfig.maxOutputTokens`
  - `stop|stop_sequences -> generationConfig.stopSequences`

### 5) Gemini Chat 图片与文件转换丢参/错形
- 文件：`internal/translator/gemini/openai/chat-completions/gemini_openai_request.go`
- 修复：
  - 新增 `parseDataURI`，拆分 `mime` 与纯 base64 数据
  - 远程图片 URL 映射到 `fileData.fileUri`
  - 新增 `guessMimeTypeFromURL` 补 `fileData.mimeType`

### 6) Gemini Chat/Responses 缺失 `tool_choice` 映射
- 文件：
  - `internal/translator/gemini/openai/chat-completions/gemini_openai_request.go`
  - `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go`
- 修复：
  - `auto -> AUTO`
  - `none -> NONE`
  - `required -> ANY`
  - 指定函数名映射到 `allowedFunctionNames`

### 7) Gemini -> OpenAI `finish_reason` 映射不准 + 多候选读取错误
- 文件：`internal/translator/gemini/openai/chat-completions/gemini_openai_response.go`
- 修复：
  - 新增 `mapGeminiFinishReason`
  - `MAX_TOKENS -> length`，并保留 `native_finish_reason=max_tokens`
  - 流式场景按当前 candidate 读取 `finishReason`，不再固定读取 `candidates.0`

### 8) Claude Responses 参数透传补齐
- 文件：`internal/translator/claude/openai/responses/claude_openai-responses_request.go`
- 修复：
  - 增加 `input` 字符串到用户消息映射
  - 增加 `temperature/top_p/stop` 映射

### 9) Claude Chat 远程图片 URL 丢失
- 文件：`internal/translator/claude/openai/chat-completions/claude_openai_request.go`
- 修复：非 data URL 的 `image_url` 映射为 Claude `source.type=url`

### 10) `HasResponseTransformer` 方向判断兼容增强
- 文件：`sdk/translator/registry.go`
- 修复：`from->to` 与 `to->from` 双向判定
- 结果：避免“可翻译却被判定不存在”问题

### 11) Gemini / Gemini-CLI 工具响应对象保真
- 文件：
  - `internal/translator/gemini/openai/chat-completions/gemini_openai_request.go`
  - `internal/translator/gemini-cli/openai/chat-completions/gemini-cli_openai_request.go`
- 修复：`functionResponse.response.result` 对象走 `SetRaw`，基础类型走 `Set`
- 结果：工具结果对象不再退化成 JSON 字符串

### 12) Responses -> Chat 不再丢弃 built-in tools
- 文件：`internal/translator/openai/openai/responses/openai_openai-responses_request.go`
- 修复：保留 `web_search` / `file_search` 等内建工具定义，不再静默忽略
- 结果：跨端点转换时工具能力信息保真

### 13) Gemini-CLI Chat 请求参数对齐补齐
- 文件：`internal/translator/gemini-cli/openai/chat-completions/gemini-cli_openai_request.go`
- 修复：
  - `max_completion_tokens|max_tokens -> request.generationConfig.maxOutputTokens`
  - `stop|stop_sequences -> request.generationConfig.stopSequences`
  - `tool_choice -> request.toolConfig.functionCallingConfig`
  - `image_url` 同时支持 `data:` 与远程 URL（远程映射到 `fileData.fileUri`）
  - 工具结果为 JSON 字符串时尝试反序列化后保留对象结构

### 14) Gemini-CLI -> OpenAI Chat 响应多候选与 finish_reason 对齐
- 文件：`internal/translator/gemini-cli/openai/chat-completions/gemini-cli_openai_response.go`
- 修复：
  - 不再固定读取 `candidates.0`，按 `response.candidates` 遍历输出 chunk
  - `MAX_TOKENS -> finish_reason=length`，并保留 `native_finish_reason=max_tokens`
  - 对 `stop/safety` 等原因进行 OpenAI 语义映射

### 15) OpenAI -> Gemini 响应多候选不再覆盖到 `candidates.0`
- 文件：`internal/translator/openai/gemini/openai_gemini_response.go`
- 修复：
  - 流式与非流式均按 choice index 写入 `candidates.{index}`
  - 消除 `n>1` 场景下“后写覆盖前写”的问题

### 16) Gemini -> OpenAI Request 细节补齐
- 文件：`internal/translator/openai/gemini/openai_gemini_request.go`
- 修复：
  - `generationConfig.stop` 字符串输入兼容
  - `functionCallingConfig.mode=ANY + allowedFunctionNames=[name]` 映射为 OpenAI 指定函数 `tool_choice` 对象
  - `functionResponse` 的 `tool_call_id` 优先按 `name` 精确匹配，减少错绑

### 17) Claude -> OpenAI Request 停止词别名兼容
- 文件：`internal/translator/openai/claude/openai_claude_request.go`
- 修复：`stop_sequences` 之外，新增 `stop` 字段兼容（字符串/数组）

### 18) Gemini -> OpenAI Responses（非流式）多候选输出补齐
- 文件：`internal/translator/gemini/openai/responses/gemini_openai-responses_response.go`
- 修复：
  - 非流式聚合不再固定 `candidates.0`
  - 按所有 `candidates[*].content.parts` 生成 `output` 项，避免多候选文本被静默丢弃
  - 保持候选内 `function_call/reasoning/message` 的输出语义

### 19) Gemini -> OpenAI Responses（流式）多候选状态机对齐
- 文件：`internal/translator/gemini/openai/responses/gemini_openai-responses_response.go`
- 修复：
  - 流式路径按 `candidates[*]` 逐个处理，不再固定 `candidates.0`
  - 新增按 candidate 维度的 message/reasoning 状态，避免 `n>1` 时互相覆盖
  - `response.completed` 聚合阶段按 `output_index` 汇总所有 candidate 的 message/reasoning/function_call
  - 补充流式多候选回归测试，覆盖 `response.output` 双候选文本输出

## 新增测试
- `internal/translator/openai/openai/responses/openai_openai-responses_request_test.go`
- `internal/translator/gemini/openai/responses/gemini_openai-responses_request_test.go`
- `internal/translator/gemini/openai/chat-completions/gemini_openai_request_test.go`
- `internal/translator/gemini/openai/chat-completions/gemini_openai_response_test.go`
- `internal/translator/gemini/openai/responses/gemini_openai-responses_response_test.go`
- `internal/translator/gemini-cli/openai/chat-completions/gemini-cli_openai_request_test.go`
- `internal/translator/gemini-cli/openai/chat-completions/gemini-cli_openai_response_test.go`
- `internal/translator/claude/openai/responses/claude_openai-responses_request_test.go`
- `internal/translator/claude/openai/chat-completions/claude_openai_request_test.go`
- `internal/translator/openai/gemini/openai_gemini_request_test.go`
- `internal/translator/openai/gemini/openai_gemini_response_test.go`

## 验证命令与结果
- `go test ./internal/translator/...`：通过
- `go test ./sdk/api/handlers/openai/...`：通过
- `go test ./internal/runtime/executor/... -run "OpenAI|compat|Translator"`：通过
