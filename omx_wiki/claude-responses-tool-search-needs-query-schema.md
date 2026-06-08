---
title: "Claude Responses Tool Search Needs Query Schema"
tags: ["claude", "openai-responses", "tool-search", "deferred-tools", "translator"]
created: 2026-06-08T13:39:50.668Z
updated: 2026-06-08T14:20:40.848Z
sources: ["https://github.com/router-for-me/CLIProxyAPI/issues/3361", "https://github.com/router-for-me/CLIProxyAPI/issues/3361#issuecomment-4649556494", "https://github.com/router-for-me/CLIProxyAPI/issues/3008", "internal/translator/claude/openai/responses/claude_openai-responses_request.go", "internal/translator/claude/openai/responses/claude_openai-responses_response.go", "internal/runtime/executor/xai_executor.go"]
links: []
category: debugging
confidence: high
schemaVersion: 1
---

# Claude Responses Tool Search Needs Query Schema

### [Translator] Claude Responses Tool Search Needs Query Schema

**Problem**
When OpenAI Responses requests with deferred tools are routed to Claude models, `ToolSearch` can be called without the required `query` argument, producing `InputValidationError: The required parameter "query" is missing`. Deferred `Task*` tools such as `TaskCreate`, `TaskGet`, `TaskList`, `TaskOutput`, `TaskStop`, and `TaskUpdate` may then fail to load usable schemas.

**Cause**
`internal/translator/claude/openai/responses/claude_openai-responses_request.go` did not have a dedicated `type:"tool_search"` conversion. A bare `{"type":"tool_search"}` could be dropped because it had no `name`; a named tool_search could be passed through raw without Claude `input_schema.required=["query"]`. The same path also did not replay `tool_search_call` / `tool_search_output` history into Claude `tool_use` / `tool_result` messages or activate tool definitions returned by `tool_search_output.tools`.

**Impact**
Claude models behind the OpenAI Responses compatibility layer receive incomplete tool-search affordances. They can emit invalid `ToolSearch` calls and never activate deferred Task or namespace tool schemas on follow-up turns.

**Resolution**
Track under GitHub issue #3361. The local fix maps Responses `tool_search` into a Claude-compatible function tool named `ToolSearch` by default, preserving explicit custom names, with an object `input_schema` requiring string `query`. It also replays `tool_search_call` into Claude `tool_use`, replays `tool_search_output` into Claude `tool_result`, loads discovered tools from `tool_search_output.tools` and compatible `results` wrappers, and converts Claude `ToolSearch` `tool_use` back to Responses `tool_search_call` in streaming and non-streaming responses. Verified with `go test -count=1 ./internal/translator/claude/openai/responses`, `go test -count=1 ./internal/translator/openai/claude`, and `go build -o test-output ./cmd/server` followed by removing `test-output`. Do not confuse this with issue #3769, which fixed empty `function.name` recovery in OpenAI-to-Claude response conversion.

**References**
Issues: https://github.com/router-for-me/CLIProxyAPI/issues/3361, https://github.com/router-for-me/CLIProxyAPI/issues/3008
Comment: https://github.com/router-for-me/CLIProxyAPI/issues/3361#issuecomment-4649556494
Code: `internal/translator/claude/openai/responses/claude_openai-responses_request.go`, `internal/translator/claude/openai/responses/claude_openai-responses_request_test.go`, `internal/translator/claude/openai/responses/claude_openai-responses_response.go`, `internal/translator/claude/openai/responses/claude_openai-responses_response_test.go`, `internal/runtime/executor/xai_executor.go`
