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

- 本地仓库：`C:\Users\15461\Documents\Github\CLIProxyAPI\CLIProxyAPI-local-build`
- Fork：`git@github.com:he1016060110/CLIProxyAPI.git`
- 分支：`fix/codex-responses-call-id-normalization`
- PR：`https://github.com/router-for-me/CLIProxyAPI/pull/4041`
- 服务器：`root@67.230.184.248`
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

## 2026-06-30 Cursor 文件搜索补丁

当前用户实际使用的服务端入口是：

- `https://67.230.184.248/v1`
- 服务器：`root@67.230.184.248`
- 容器：`cli-proxy-api`

这轮排查确认：

- Cursor 请求确实进入 `/v1/chat/completions` 和 Codex OpenAI translator。
- `converted_output_matched=true` 基本稳定，说明工具输出和工具调用的 `call_id` 配对不是这次的主问题。
- 工具结果返回后 `converted_tool_choice=<missing>`，说明 `e55f5497` 的“工具输出后不再强制 required”已在服务端生效。
- 实际失败集中在 Cursor 本地文件搜索参数，例如 `rg` 返回 Windows 路径错误：`系统找不到指定的路径。 (os error 3)`。
- 之前 trace 没记录 Cursor `Glob` 的 `glob_pattern` / `target_directory`，导致日志只能看到 `keys=glob_pattern,target_directory`，看不到真正出错路径。
- 之前错误分类会因为源码中出现 `error` / `failed` 字样把正常文件内容误判成工具失败，影响排查判断。
- 部署 trace 补丁后再次复现，看到新的空结果来自 `rg` 参数语义不匹配：Codex 会传 `path=.../file.ts` 或 `path=repoRoot, glob=*.{js,ts,tsx}`，但 Cursor 的文件搜索 wrapper 更像“目录根 + 递归 glob”，这些参数会返回 `No_matches_found` / `No_files_with_matches_found`，即使本机原生 `rg` 对同样文件和 pattern 有命中。

本次补丁：

- `cursor_tool_trace` 同时记录 `glob_pattern`、`target_directory`、`workspace_path`、`relativeWorkspacePath`、`include_pattern`、`exclude_pattern` 等 Cursor 文件工具常见字段。
- `classifyToolOutput` 不再把普通源码里的 `error` / `failed` 字样直接判为失败，只识别更明确的工具失败前缀和路径错误。
- OpenAI agent tool-use instruction 增加 Cursor 文件搜索提示：只使用已观察到的 workspace root 或目录；`Glob` 明确给出真实 `target_directory` + `glob_pattern`；`rg/search` 从已验证目录运行，避免硬造 Windows 绝对路径。
- Chat Completions 流式转换层对 `rg` 参数做兼容归一化：如果 `path` 指向文件，改成 `path=所在目录` 且 `glob=文件名`；如果 `glob` 是 `*.tsx` / `*.{js,ts,tsx}` 这种非递归模式，改成 `**/*.tsx` / `**/*.{js,ts,tsx}` 后再交给 Cursor。

本地验证：

```text
docker run --rm -v "${PWD}:/app" -w /app -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test ./internal/util ./internal/translator/codex/openai/chat-completions ./internal/translator/codex/openai/responses
```

### 2026-06-30 Cursor rg glob 前缀补丁

复现日志显示这次报错来自 Cursor `rg` 工具，而不是 `Glob` 工具本身：

```text
item_name=rg item_arg_hint=path=C:\Users\15461\Documents\canvas\AgentsTeam\OnsiteArtCanvas,pattern=production-proposals|production-locks|sourceIntakeDecision|updateProductionProposalStatus,glob=server/**/*.js
orig_last_tool_failure=not-found:Error_running_tool:_rg:_:_IO_error_for_operation_on_:_系统找不到指定的路径。_(os_error_3)
```

原因：

- Cursor 的 `rg` wrapper 更稳定的形态是“已验证目录 path + 相对该目录的 glob”。
- `path=repoRoot, glob=server/**/*.js` 会在部分 Cursor 本地工具链里触发 Windows 路径解析错误。
- 应改成 `path=repoRoot\server, glob=**/*.js`。

本次补丁：

- `normalizeCursorSearchToolArguments` 会把 `glob` 里的目录前缀迁移到 `path`。
- 例如 `path=C:\repo, glob=server/**/*.js` 会改成 `path=C:\repo\server, glob=**/*.js`。
- 如果 `path` 已经是 `C:\repo\server`，则不会重复拼接 `server`。
- 保留原先的非递归 glob 修复：`*.test.tsx` 仍会改成 `**/*.test.tsx`。

本地验证：

```text
docker run --rm -v "${PWD}:/app" -w /app -e GOPROXY=https://goproxy.cn,direct -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test ./internal/translator/codex/openai/chat-completions
```

### 2026-07-01 Cursor Glob 过宽模式补丁

复现日志显示新的失败不是空 `target_directory`，而是 Cursor 拒绝过宽 glob：

```text
item_name=Glob item_arg_hint=glob_pattern=**/*,target_directory=C:\Users\15461\Documents\canvas\AgentsTeam\OnsiteArtCanvas\server\modules\chat-session
orig_last_tool_failure=error:Error_running_tool:_Glob_pattern_"**/*"_matches_every_file_and_is_not_allowed._Use_a_more_specific_glob_or_no_glob.
```

同一轮截图里的 i18n 搜索参数本身有 `target_directory`：

```text
glob_pattern=**/*locale*,target_directory=...\OnsiteArtCanvas\src
glob_pattern=**/*i18n*,target_directory=...\OnsiteArtCanvas\src
glob_pattern=**/translations*.json,target_directory=...\OnsiteArtCanvas
```

本次补丁：

- `Glob` 工具参数也进入流式参数抑制和归一化链路。
- `Glob` 的 `glob_pattern=**/*` 会改成 `**/*.{js,jsx,ts,tsx,json,md,mdx,css,scss,yml,yaml}`，避免 Cursor 直接拒绝。
- `Glob` 的 `glob_pattern=server/**/*.js` 会改成 `target_directory=...\server, glob_pattern=**/*.js`。
- `rg` 的 `glob=**/*` 会清空 glob，让它只按 `path + pattern` 搜索目录。
- 工具提示明确禁止 `Glob(**/*)`，要求用具体扩展名 glob 或命令行 `rg --files` 做广义列文件。
- trace 的 `not-found` / `outside-workspace` 分类只在真正工具错误或 `<workspace_result>` 结果中触发，避免把源码内容里的普通文案误判为工具失败。

本地验证：

```text
docker run --rm -v "${PWD}:/app" -w /app -e GOPROXY=https://goproxy.cn,direct -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test ./internal/util ./internal/translator/codex/openai/chat-completions ./internal/translator/codex/openai/responses
```

## 2026-06-30 Claude Code Ultracode 兼容补丁

问题：

- Claude Code UI 会显示 `Ultracode`，但代理原本只识别 `minimal` / `low` / `medium` / `high` / `xhigh` / `max`。
- 当客户端把 `ultracode` 放进 `reasoning_effort`、`reasoning.effort` 或 Claude `output_config.effort` 时，旧逻辑有的路径会直接透传 `ultracode`，有的路径会把 `xhigh` 误升成 `max`。

本次补丁：

- 在 `internal/thinking` 统一把 `ultracode` / `ultra-code` / `ultra_code` 归一化为 `xhigh`。
- Claude adaptive effort 按目标模型支持列表映射：`claude-opus-4-8` 支持 `xhigh` 时保留 `xhigh`；`claude-sonnet-4-6` 不支持 `xhigh` 时降为 `high`，不会误变成 `max`。
- 覆盖 OpenAI Chat Completions、OpenAI Responses、Claude、Codex 相关转换路径，避免某一路绕过统一归一化。

本地验证：

```text
docker run --rm -v "${PWD}:/app" -w /app -e GOPROXY=https://goproxy.cn,direct -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test ./internal/thinking ./internal/translator/claude/openai/chat-completions ./internal/translator/claude/openai/responses ./internal/translator/codex/openai/chat-completions
docker run --rm -v "${PWD}:/app" -w /app -e GOPROXY=https://goproxy.cn,direct -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test ./internal/translator/claude/gemini ./internal/translator/codex/claude ./internal/translator/openai/claude
```

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

## 2026-07-01 Cursor Subagent 创建失败

用户反馈 Cursor 中出现：

```text
New subagent
Couldn't start
```

服务器 trace 显示代理到 Cursor 的响应链路本身是 200，但 Cursor 本地工具返回了参数校验错误：

```text
item_name=Subagent item_type=function_call
item_arg_hint=keys=cloud_base_branch,description,environment,file_attachments,interrupt,model,prompt,readonly,resume,run_in_background,subagent_type
orig_last_tool_class=error
orig_last_tool_failure=error:Error:_Invalid_arguments:_cloud_base_branch:_cloud_base_branch_may_only_be_specified_when_environment_equals_cloud
```

结论：

- 这不是服务器限流或 HTTP 失败。
- Codex/Cursor 的 `Subagent` 调用里 `environment` 不是 `cloud`，但参数仍带了 `cloud_base_branch`。
- Cursor 本地 Subagent 工具明确拒绝这种组合，于是 UI 显示 `Couldn't start`。

修复：

- 将 `Subagent` 纳入 Cursor 参数规范化链路。
- `environment == "cloud"` 时保留 `cloud_base_branch`。
- 其他环境或未声明环境时自动删除 `cloud_base_branch`，避免 Cursor 本地工具拒绝创建。
- Agent 工具提示中补充约束：只有 `environment is cloud` 时才包含 `cloud_base_branch`。
- trace 摘要增加 Subagent 字段：`environment`、`cloud_base_branch`、`subagent_type`、`run_in_background`、`readonly`、`resume`、`model`、`description`；不记录 `prompt` 正文。

部署记录：

- 服务器镜像：`cli-proxy-api:cursor-subagent-guard-20260701`
- 当前容器：`cli-proxy-api`
- 旧容器备份：`cli-proxy-api.before-subagent-guard-20260630234355`
- `CLIPROXY_CURSOR_TOOL_TRACE=1` 仍然开启，用于继续观察 Cursor 本地工具失败。

## 2026-07-01 Cursor 搜索与 GPT xhigh 监控显示热修

本次同时覆盖两个问题：

- Cursor 搜索工具参数补丁：`rg` / `Glob` / `Subagent` 的参数需要在 Chat Completions 与 Responses 两条 Codex 输出路径中稳定归一化。
- GPT thinking 监控显示：用户实际发送 `xhigh`，但 CPAM 监控表有的只显示 `gpt-5.5`，有的显示 `gpt-5.5-extra`，且“实际调用”子行没有显示最终 thinking 强度。

后端修复：

- `UsageReporter` 在最终上游 payload 已确认 `xhigh` 时，将 GPT 基础模型的 usage alias 提升为 `gpt-5.5-extra`，实际上游模型仍保持 `gpt-5.5`。
- usage queue 增加 `actual_model` 原始字段，并在给 CPAM 当前消费链路的 `model` / `resolved_model` 展示值中写入 `gpt-5.5 (xhigh)`；因此监控页可显示：
  - 主模型：`gpt-5.5-extra`
  - 实际调用：`gpt-5.5 (xhigh)`
- `.gitignore` 增加 `.gomodcache/`，避免 Docker 化 Go 测试的本地模块缓存进入提交。

本地 Docker 化回归：

```text
docker run --rm -v "${PWD}:/app" -w /app -e GOPROXY=https://goproxy.cn,direct -e GOMODCACHE=/app/.gomodcache -e GOCACHE=/app/.gocache golang:1.26-bookworm go test -count=1 ./internal/util ./internal/thinking ./internal/translator/codex/openai/chat-completions ./internal/translator/codex/openai/responses ./internal/translator/claude/openai/chat-completions ./internal/translator/claude/openai/responses ./internal/translator/claude/gemini ./internal/translator/codex/claude ./internal/translator/openai/claude ./internal/runtime/executor/helps ./internal/redisqueue ./sdk/api/handlers
```

结果：

```text
ok ./internal/util
ok ./internal/thinking
ok ./internal/translator/codex/openai/chat-completions
ok ./internal/translator/codex/openai/responses
ok ./internal/translator/claude/openai/chat-completions
ok ./internal/translator/claude/openai/responses
ok ./internal/translator/claude/gemini
ok ./internal/translator/codex/claude
ok ./internal/translator/openai/claude
ok ./internal/runtime/executor/helps
ok ./internal/redisqueue
ok ./sdk/api/handlers
```

构建与部署：

- 二进制：`dist/CLIProxyAPI`
- 二进制 SHA256：`C5C3D95783211C598E6831AEDFC1D0C83680B665D37061854875215AEBA2CFCE`
- 镜像：`cli-proxy-api:cursor-search-thinking-monitor-20260701-0936`
- 镜像 ID：`sha256:61102fd5f39965df12659ebe9d108ea81fe53bd6d498036502a3aca0dd8caa31`
- 当前容器：`cli-proxy-api`
- 当前容器 ID：`a02e6e7f722e8c087699294f90aeff6044a583d85625d6f50a607be6a6535545`
- 服务器：`67.230.184.248`
- 端口沿用旧容器：
  - `127.0.0.1:21451->11451`
  - `127.0.0.1:21455->1455`
  - `127.0.0.1:21121->51121`
  - `127.0.0.1:24545->54545`
  - `127.0.0.1:28085->8085`
  - `127.0.0.1:28317->8317`
- 挂载沿用旧容器：
  - `/opt/CLIProxyAPI/config.yaml:/CLIProxyAPI/config.yaml`
  - `/opt/CLIProxyAPI/auths:/root/.cli-proxy-api`
  - `/opt/CLIProxyAPI/logs:/CLIProxyAPI/logs`
- `CLIPROXY_CURSOR_TOOL_TRACE=1` 仍然开启，用于继续观察 Cursor 工具参数。定位稳定后应关闭，避免日志刷屏。

远端冒烟：

```text
GET /v1/models without key -> 401
POST /v1/chat/completions forced rg, reasoning_effort=xhigh -> 200, rg tool count = 1
POST /v1/chat/completions forced Glob, reasoning_effort=xhigh -> 200, Glob tool count = 1
POST /v1/responses reasoning.effort=xhigh -> 200, response model = gpt-5.5
CPAM /v0/management/usage/realtime -> 200
CPAM recent model = gpt-5.5-extra
CPAM recent resolved_model = gpt-5.5 (xhigh)
CPAM recent endpoint = POST /v1/responses
```

trace 摘要：

```text
chat request converted_model=gpt-5.5 converted_reasoning_effort=xhigh converted_tool_choice=function:rg orig_model=gpt-5.5 orig_reasoning_effort=xhigh
codex response event=response.output_item.done item_name=rg item_type=function_call item_arg_hint=path=C:,pattern=production-proposals,glob=server/**/*.js
chat request converted_model=gpt-5.5 converted_reasoning_effort=xhigh converted_tool_choice=function:Glob orig_model=gpt-5.5 orig_reasoning_effort=xhigh
codex response event=response.output_item.done item_name=Glob item_type=function_call item_arg_hint=path=C:,pattern=**/*.{js,jsx,mjs,cjs}
responses request converted_model=gpt-5.5 converted_reasoning_effort=xhigh orig_model=gpt-5.5 orig_reasoning_effort=xhigh
```

回滚点：

- 上一版容器：`cli-proxy-api.before-thinking-monitor-20260701093600`
- 上一版镜像：`cli-proxy-api:cursor-search-thinking-20260701-0922`
- 更早容器：`cli-proxy-api.before-search-thinking-20260701012600`
- 更早镜像：`cli-proxy-api:cursor-subagent-guard-20260701`

如需回滚到上一版：

```text
docker rm -f cli-proxy-api
docker rename cli-proxy-api.before-thinking-monitor-20260701093600 cli-proxy-api
docker start cli-proxy-api
```

### 2026-07-01 10:12 GPT effort 别名推断与 CPAM 展示补充

用户反馈每次在 Cursor 里选择的都是 xhigh，但请求监控有时只显示 `gpt-5.5` 或 `gpt-5.5-extra`，实际调用行没有显示 thinking 强度。

本次重新查线上请求后确认：

- Cursor 原始 Chat Completions 请求没有单独携带 `reasoning_effort` 字段。
- xhigh 是通过模型别名表达的：`orig_model=gpt-5.5-extra`。
- 旧逻辑只在显式 `reasoning_effort` / `reasoning.effort` / suffix 中取值，缺字段时回落到默认 `medium`，导致 usage 和 trace 有时显示不一致。

本次源码修复：

- `internal/thinking` 新增 GPT 模型别名推断：
  - `gpt-5.5-extra` -> `xhigh`
  - `gpt-5.5-high` -> `high`
  - `gpt-5.5-medium` -> `medium`
  - `gpt-5.5-low` -> `low`
- 显式请求字段仍优先于模型别名，例如 `model=gpt-5.5-extra` 且 `reasoning_effort=low` 时按 `low`。
- Codex OpenAI Chat Completions translator 在请求体缺失 effort 时从原始 model alias 推断，避免再默认成 `medium`。
- handler metadata 也使用同一套推断结果，保证 usage 上下文和实际转换一致。

本地 Docker 化回归：

```text
docker run --rm -v "${PWD}:/app" -w /app golang:1.26-alpine go test ./internal/thinking ./sdk/api/handlers ./internal/translator/codex/openai/chat-completions
```

结果：

```text
ok ./internal/thinking
ok ./sdk/api/handlers
ok ./internal/translator/codex/openai/chat-completions
```

构建与部署：

- 二进制：`dist/CLIProxyAPI`
- 二进制 SHA256：`6A9A20A1AF64EE7A327C66A8B86E85431D5F6497A9E361E9D759C49393C44552`
- 镜像：`cli-proxy-api:cursor-xhigh-monitor-20260701-1006`
- 镜像 ID：`sha256:055734c74f5a779ecf231f36f293df6e05efbffcb83f508ee573ce0dc6e7520c`
- 当前容器：`cli-proxy-api`
- 当前容器 ID：`64967b775d0e89f2970e0dae24960f892f7ef41396bf18b657447ec39429c6af`
- 服务器：`67.230.184.248`
- 端口、网络和挂载沿用旧容器。
- trace 冒烟后已重启为 `CLIPROXY_CURSOR_TOOL_TRACE=0`，避免继续刷日志。

线上 trace 冒烟结果：

```text
orig_model=gpt-5.5-extra
orig_reasoning_effort=<missing>
converted_model=gpt-5.5
converted_reasoning_effort=xhigh
```

Cursor 搜索工具冒烟：

```text
item_name=rg item_type=function_call item_arg_hint=path=...,pattern=...,glob=**/*.{ts,tsx,json}
orig_last_tool_class=empty-glob
converted_output_matched=true
converted_reasoning_effort=xhigh
```

说明 `rg` 工具调用、空 Glob 结果回传、工具输出配对和 xhigh 推断都在同一轮 trace 中正常工作。`empty-glob` 表示 Cursor 本地工具确实执行并返回空结果，不是代理丢失工具结果。

CPAM 同步部署：

- 镜像：`cpa-manager:reasoning-effort-display-20260701-1008`
- 镜像 ID：`sha256:bb14a662ca0ea83938e1859f34161227d0f1de3ba009967dce94f6afa00e6db0`
- 当前容器：`cpa-manager`
- 当前容器 ID：`54cd5cfe3c20a744f27b8f3aa4f2a522a86d2860327abac8602ef356a4fe93e7`
- API 验证：`/v0/management/usage/realtime` 最新记录已返回 `reasoning_effort=xhigh`，`resolved_model=gpt-5.5 (xhigh)`。
- 页面展示逻辑会对已经带 `(xhigh)` 的 resolved model 去重，因此显示为一次：`实际调用：gpt-5.5 (xhigh)`。

延迟结论：

- 截图中的 20 秒到 1 分钟级延迟来自完整请求耗时，不是监控页渲染耗时。
- 线上最新多条 xhigh 请求输入量约 `46K` 到 `177K` tokens，延迟约 `32s` 到 `66s`，主要受超大上下文、工具往返和上游生成耗时影响。
- xhigh 会增加推理工作量，不能用来降低延迟；它只是提高 thinking 强度。

回滚点：

- 代理当前 trace-off 前一版：`cli-proxy-api.rollback-trace-20260701-1012`，镜像同为 `cli-proxy-api:cursor-xhigh-monitor-20260701-1006`，但 trace 开启。
- 代理上一版业务镜像：`cli-proxy-api.rollback-20260701-1006`，镜像 `cli-proxy-api:cursor-search-thinking-monitor-20260701-0936`。
- CPAM 当前上一版：`cpa-manager.rollback-20260701-1008`，镜像 `cpa-manager:reasoning-effort-display-20260701-1006`。
- CPAM 原始上一版：`cpa-manager.rollback-20260701-1006`，镜像 `seakee/cpa-manager:latest`。

回滚命令示例：

```text
docker rm -f cli-proxy-api
docker rename cli-proxy-api.rollback-20260701-1006 cli-proxy-api
docker start cli-proxy-api

docker rm -f cpa-manager
docker rename cpa-manager.rollback-20260701-1008 cpa-manager
docker start cpa-manager
```
