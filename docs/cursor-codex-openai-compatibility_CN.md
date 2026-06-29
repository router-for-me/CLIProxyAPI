# Cursor / Codex OpenAI 兼容修复记录

本文记录 2026-06-29 针对 Cursor 使用本代理访问 Codex/OpenAI 兼容接口时遇到的问题、修复点、部署方式和验证结果。后续如果继续改这条链路，请把新增修改追加到本文，避免服务器热修和源码记录脱节。

## 背景

Cursor 通过自定义 OpenAI API 指向 CLIProxyAPI。实际请求主要落在：

- `POST /v1/chat/completions`
- 部分 OpenAI Responses 直连兼容路径

初始问题包括：

- OpenAI/Codex 上游拒绝过长 `call_id`：`input[4].call_id` 长度 83，超过最大 64。
- Cursor Agent 经常只输出“我继续...”但不真正调用工具。
- Codex 上游返回 `custom_tool_call` 时，代理没有转换成 OpenAI Chat Completions 的 `tool_calls`，导致 Cursor 看不到工具调用。
- 修复 `required` 后又出现工具循环：拿到 `ReadFile` / `Glob` 工具结果后仍被强制继续调用工具，不能自然结束。

## 本地仓库与服务器

- 本地仓库：`C:\Users\15461\Documents\Codex\2026-06-29\files-mentioned-by-the-user-9277890fb8eab448144718dde620801f\work\CLIProxyAPI-local-build`
- Fork：`git@github.com:he1016060110/CLIProxyAPI.git`
- 分支：`fix/codex-responses-call-id-normalization`
- PR：`https://github.com/router-for-me/CLIProxyAPI/pull/4041`
- 服务器：`root@192.220.55.173 -p 36206`
- 运行方式：Docker 容器 `cli-proxy-api`
- 当前修复镜像基线：`cli-proxy-api:cursor-fix-84e19d85`

服务器没有 Go 环境，因此构建方式是本地 Docker 编译 Linux amd64 二进制，再用热修镜像覆盖原镜像里的 `/CLIProxyAPI/CLIProxyAPI`：

```dockerfile
FROM eceasy/cli-proxy-api:latest

COPY dist/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
RUN chmod +x /CLIProxyAPI/CLIProxyAPI
```

## 已提交修改

### 57aa827c - normalize long codex responses call ids

问题：Cursor 会把上一轮工具调用结果带回较长 `call_id`，OpenAI/Codex Responses 上游最大只接受 64 字符。

修改：

- 新增 `internal/util/openai_responses_call_id.go`
- 在 Codex OpenAI Responses 请求转换路径调用 `NormalizeOpenAIResponsesInputCallIDs`
- 在 Codex executor 发送前兜底归一化
- 对超长 `call_id` 做稳定短 ID 映射，保证同一请求里的 `function_call` 和 `function_call_output` 仍能配对

关键行为：

- `<=64` 的 ID 不变
- `>64` 的 ID 缩短为稳定哈希后缀格式
- 同一个长 ID 每次得到同一个短 ID

### 0a015f25 - nudge codex agent tool use for openai clients

问题：Cursor Agent 有时只输出“我会继续...”而不调用工具。

修改：

- 新增 `internal/util/openai_agent_tool_prompt.go`
- 在 OpenAI Chat Completions 和 Responses 转 Codex 请求时，给有 function tools 的请求追加工具使用提示

提示目标：

- 当下一步需要读文件、改文件、跑命令时，要求模型在当前响应里调用工具
- 避免只用自然语言承诺稍后行动

### a1f782be - require function tools for codex agent turns

问题：仅追加提示仍不足以稳定触发 Cursor 工具调用。

修改：

- `RequireOpenAIAgentFunctionToolChoice` 将 function tools + `tool_choice:auto` / missing 的请求转成 `tool_choice:"required"`
- 保留显式 `tool_choice:"none"`
- 保留指定具体 function 的 `tool_choice`

注意：后续发现该规则需要收窄，见“当前待部署修复”。

### 30384638 - bridge legacy openai function calls to codex

问题：Cursor/旧 OpenAI 客户端可能使用 legacy `functions` / `function_call`，而不是现代 `tools` / `tool_calls`。

修改：

- `functions` 转 `tools`
- `function_call` 转 `tool_choice`
- assistant 的 legacy `function_call` 转 Responses `function_call`
- `role:"function"` 工具结果转 `function_call_output`
- 返回给 legacy 客户端时输出 `delta.function_call` 和 `finish_reason:"function_call"`

目的：

- 同时兼容现代 OpenAI tools 和旧 OpenAI functions

### c4d4f910 - add cursor tool-call trace diagnostics

问题：需要确认 Cursor 实际发来的请求是否有 tools/functions，以及 Codex 上游实际返回普通文本还是工具调用。

修改：

- 新增 Chat Completions trace
- 环境变量 `CLIPROXY_CURSOR_TOOL_TRACE=1` 时启用
- 只记录安全摘要，不记录代码内容、文件内容、密钥

记录字段示例：

- tools/functions 数量
- `tool_choice`
- 最后一条消息 role
- 返回事件类型
- 输出 item type/name

### 97bfcae1 - trace cursor responses tool protocol

问题：最初的 `call_id` 报错来自 Responses 输入路径，trace 只覆盖 Chat Completions 不够。

修改：

- 给 Codex OpenAI Responses 请求/响应路径增加同样的安全摘要 trace
- 记录 input/tools/tool_choice 和响应事件类型

### ecd81bf0 - print cursor tool trace fields

问题：服务器日志格式没有显示 `log.WithFields` 的字段，诊断只剩固定文本。

修改：

- 将 trace fields 拼进日志 message 文本
- 仍然只记录摘要，不记录正文内容

### 84e19d85 - bridge codex custom tool calls to openai chat

问题：日志确认 Codex 上游返回：

```text
response.output_item.added item_type=custom_tool_call item_name=ApplyPatch
response.completed output_types=[reasoning message custom_tool_call]
```

但 Chat Completions 转换器只识别 `function_call`，忽略了 `custom_tool_call`，Cursor 因此看不到真正的 `tool_calls`。

修改：

- 在 `internal/translator/codex/openai/chat-completions/codex_openai_response.go` 中把：
  - `custom_tool_call.name` 映射为 Chat Completions `tool_calls[].function.name`
  - `custom_tool_call.input` 映射为 Chat Completions `tool_calls[].function.arguments`
- 同时支持：
  - `response.output_item.added`
  - `response.output_item.done`
  - `response.custom_tool_call_input.delta`
  - `response.custom_tool_call_input.done`
  - 非流式 `response.completed.output[]`

兼容边界：

- 只在“Codex 上游 -> OpenAI Chat Completions 客户端”的转换层桥接
- Codex 自己走 Responses 的访问仍保留 `custom_tool_call`，不改写成 Chat `tool_calls`

验证：

- Cursor 重试后不再只输出一句话
- 日志显示 Cursor 回传了 `role=tool`
- 后续模型继续发出 `ReadLints` / `Shell` 等 function calls

### e55f5497 - allow final answers after tool outputs

问题：修复工具调用后，Cursor 能执行工具了，但出现反复 `ReadFile` / `Glob`，例如多次读取 `最终缺失图片清单.txt L1-5`。

诊断日志显示：

```text
orig_last_role=tool
converted_tool_choice=required
```

原因：

- 前面的 `a1f782be` 把有 function tools 的请求强制成 `tool_choice:"required"`
- 当 Cursor 已经返回工具结果后，下一轮仍然被强制 `required`
- 模型不能自然输出最终回答，只能继续调用工具，于是出现一直读文件/查 glob 的循环

修复策略：

- 如果 Codex Responses `input` 的最后一项是：
  - `function_call_output`
  - `custom_tool_call_output`
- 则不再强制 `tool_choice:"required"`
- 保留原来的 `auto` 或 missing，让模型可以：
  - 继续调用工具，或
  - 直接结束回答

改动文件：

- `internal/util/openai_agent_tool_prompt.go`
- `internal/util/openai_agent_tool_prompt_test.go`
- `internal/translator/codex/openai/chat-completions/codex_openai_request_test.go`
- `internal/translator/codex/openai/responses/codex_openai-responses_request_test.go`

新增/调整测试：

- function tools 首轮仍可转 `required`
- 最后一项是 `function_call_output` 时保持 `auto`
- 最后一项是 `custom_tool_call_output` 且原本没有 `tool_choice` 时继续保持 missing
- Chat Completions legacy function output 后不再要求 `required`
- Responses input 工具输出后不再要求 `required`

## 诊断日志说明

`CLIPROXY_CURSOR_TOOL_TRACE=1` 可临时打开 trace。已确认该 trace 不记录：

- API key
- 文件内容
- 命令输出正文
- 工具参数正文

只记录：

- 工具数量
- `tool_choice`
- 最后一条消息 role
- 最后工具输出长度
- 输出是否可粗略分类为 not-found / empty-glob / permission / error
- Codex 响应事件类型
- 工具名
- 参数长度

排查完成后应关闭该环境变量，避免日志刷屏。

部署：

- 服务器镜像：`cli-proxy-api:cursor-fix-e55f5497`
- 部署时临时开启 `CLIPROXY_CURSOR_TOOL_TRACE=1`，用于确认工具输出后的下一轮请求不再被强制 `required`
- 确认完成后应关闭 trace，避免日志刷屏

## 测试记录

已经通过过的相关测试：

```text
go test ./internal/translator/codex/openai/chat-completions ./internal/translator/codex/openai/responses
go test ./internal/runtime/executor -run "CodexExecutor|Codex.*ReasoningReplay|ReasoningReplay.*Codex|Codex.*ShortensLongCallIDs"
```

`e55f5497` 提交前已通过：

```text
go test ./internal/util ./internal/translator/codex/openai/chat-completions ./internal/translator/codex/openai/responses ./internal/runtime/executor -run "AgentToolUseInstruction|FunctionToolChoice|DoesNotRequireAfterToolOutput|LegacyFunctions|CustomToolCall|ToolCall|ToolsDefinitionTranslated|CodexExecutor|Codex.*ReasoningReplay|ReasoningReplay.*Codex|ShortensLongCallIDs"
```

注意：曾经用过过宽的 `-run ReasoningReplay`，会扫到 Antigravity 旧测试失败；那次失败和本修复无关。后续应使用更窄的 Codex executor 正则。

## 回滚方式

服务器每次替换容器前都会把旧容器 rename 成类似：

```text
cli-proxy-api.before-...
```

如果新容器异常，可停止新容器并将上一个备份容器 rename 回 `cli-proxy-api` 后启动。

镜像层面可回滚到：

- `cli-proxy-api:cursor-fix-84e19d85`
- 更早的 `cli-proxy-api:cursor-trace-ecd81bf0`
- 更早的 `cli-proxy-api:cursor-fix-30384638`

## 当前结论

- `call_id` 过长已通过稳定短 ID 修复。
- Cursor “一句话结束”已通过 `custom_tool_call` 桥接修复。
- Cursor 反复读文件的原因是 `tool_choice:"required"` 强制范围过大。
- `e55f5497` 已将规则收窄：工具结果返回后不再强制 required，让模型可以最终回答。

## 2026-06-29 继续诊断：Cursor 本地文件查找失败

用户反馈 Cursor 仍然显示“读取不到文件”，截图中表现为：

- 在当前工作区找不到 Word 或其他文件
- 去上一级或 `Documents` 下搜索
- 搜索 `最终缺失图片清单.txt` / `生成报告.txt`
- 仍然停留在“Search files attempted”

服务器 trace 显示：

```text
orig_last_role=tool
converted_tool_choice=<missing>
converted_output_matched=true
orig_last_tool_class=not-found / error / ok
```

这个结果说明：

- `e55f5497` 的修复生效了：工具结果回来后不再强制 `tool_choice:"required"`。
- 代理没有丢掉工具结果：`converted_output_matched=true`。
- Cursor 本地工具确实返回了结果，其中有 `not-found` / `error`，也有 `ok`。
- 当前问题更像是 Cursor 本地工具的工作区/路径解析失败，而不是 OpenAI/Codex 协议转换层再次丢工具。

为继续定位，新增 trace 字段：

- Codex 返回工具调用时，记录 `item_arg_hint`
  - 对 `Glob` / `ReadFile` 等记录 `path` / `file_path` / `query` / `pattern` / `cwd`
  - 对 `Shell.command` 只记录长度，不记录命令正文
- Cursor 返回工具失败时，记录 `orig_last_tool_failure`
  - 只在 `not-found` / `empty-glob` / `permission` / `outside-workspace` / `error` 等失败分类时出现
  - 成功工具输出不记录正文，避免把文件内容写进日志

下一步复现时需要看：

- `item_arg_hint` 中 Cursor 实际被要求查找的路径/模式
- `orig_last_tool_failure` 中 Cursor 本地工具返回的失败摘要
- 如果路径指向工作区外，则应调整 Cursor 打开的工作区根目录，或让任务文件进入当前 workspace；代理无法让 Cursor 本地工具越过它自己的工作区权限边界。

部署记录：

- 源码提交：`feed7d4b chore: trace cursor tool path hints`
- 服务器镜像：`cli-proxy-api:cursor-trace-feed7d4b`
- 当前容器：`cli-proxy-api`
- 旧容器备份：`cli-proxy-api.before-feed7d4b-20260629055000`
- 端口沿用旧容器：`1455` / `8085` / `8317` / `11451` / `51121` / `54545`
- 挂载沿用旧容器：
  - `/opt/CLIProxyAPI/config.yaml:/CLIProxyAPI/config.yaml`
  - `/opt/CLIProxyAPI/auths:/root/.cli-proxy-api`
  - `/opt/CLIProxyAPI/logs:/CLIProxyAPI/logs`
- `CLIPROXY_CURSOR_TOOL_TRACE=1` 仍然开启，待定位完成后关闭。
