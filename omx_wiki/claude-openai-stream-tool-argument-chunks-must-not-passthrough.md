---
title: "Claude OpenAI Stream Tool Argument Chunks Must Not Passthrough"
tags: ["claude-code", "openai-compatible", "tool-use", "streaming", "translator", "tool-search"]
created: 2026-06-08T15:48:36.822Z
updated: 2026-06-08T15:48:36.822Z
sources: ["https://github.com/router-for-me/CLIProxyAPI/issues/3769", "internal/translator/openai/claude/openai_claude_response.go", "internal/translator/openai/claude/openai_claude_response_test.go", ".omc/evidence/raw-capture-20260608-233620", ".omc/evidence/fixed-toolsearch-20260608-verify"]
links: ["claude-tool-use-requires-real-names-when-openai-streams-omit-fun.md", "claude-responses-tool-search-needs-query-schema.md"]
category: debugging
confidence: high
schemaVersion: 1
---

# Claude OpenAI Stream Tool Argument Chunks Must Not Passthrough

### [Translator] Claude OpenAI Stream Tool Argument Chunks Must Not Passthrough

**Problem**
Claude Code CLI routed through `/v1/messages?beta=true` to an OpenAI-compatible upstream repeatedly failed `ToolSearch` with `InputValidationError: The required parameter "query" is missing`. The failure reproduced in normal Claude CLI mode against local port `8317` for 54 turns and again against capture port `8327`. `--bare` is not a valid reproducer for this issue because it does not expose the same deferred tool surface.

**Cause**
The OpenAI-compatible upstream did send complete streaming `tool_calls[].function.arguments` chunks for `ToolSearch`, including `{"max_results":6,"query":"TaskCreate TaskGet TaskList TaskOutput TaskStop TaskUpdate"}`. In `internal/translator/openai/claude/openai_claude_response.go`, argument-only chunks were accumulated without emitting a Claude event, leaving the result slice nil. `sdk/translator.Registry.TranslateStream` treats nil output as "fallback to raw body", so those OpenAI `data: {"choices":[...chat.completion.chunk...]}` chunks leaked into the downstream Anthropic SSE response instead of becoming `content_block_delta/input_json_delta`.

**Impact**
Claude Code saw a `content_block_start` for `ToolSearch` with `input:{}` while the argument chunks were interleaved as raw OpenAI SSE. The client failed to assemble the tool input and executed `ToolSearch` without `query`, preventing deferred `TaskCreate`, `TaskGet`, `TaskList`, `TaskOutput`, `TaskStop`, and `TaskUpdate` schemas from loading.

**Resolution**
For OpenAI-to-Claude streaming response conversion, return a non-nil empty slice for consumed no-output chunks, and stream tool argument deltas as Claude `input_json_delta` immediately after the tool block has started. Track emitted argument bytes to avoid duplicate final deltas, while preserving final repair for fully unstreamed accumulated arguments.

**References**
Issue: https://github.com/router-for-me/CLIProxyAPI/issues/3769
Code: `internal/translator/openai/claude/openai_claude_response.go`, `internal/translator/openai/claude/openai_claude_response_test.go`
Evidence before fix: `.omc/evidence/raw-capture-20260608-233620/claude-cli-on-8327`, `D:\cli-proxy\.cli-proxy-api\logs\v1-messages-2026-06-08T233751-49512d48.log`
Evidence after fix: `.omc/evidence/fixed-toolsearch-20260608-verify`, `D:\cli-proxy\.cli-proxy-api\logs\v1-messages-2026-06-08T234649-79b10b5c.log`
Verification: `go test -count=1 ./internal/translator/openai/claude`, `go test -count=1 ./internal/translator/claude/openai/responses`, `go build -o test-output ./cmd/server` followed by removing `test-output`, and normal Claude CLI through local `8327` successfully loading all six Task tool schemas via `ToolSearch`.
