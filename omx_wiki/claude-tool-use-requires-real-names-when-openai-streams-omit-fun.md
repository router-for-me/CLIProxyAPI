---
title: "Claude Tool Use Requires Real Names When OpenAI Streams Omit Function Name"
tags: ["claude-code", "openai-compatible", "tool-use", "streaming", "translator", "non-streaming"]
created: 2026-06-08T11:55:23.151Z
updated: 2026-06-08T13:33:19.998Z
sources: ["https://github.com/router-for-me/CLIProxyAPI/issues/3748", "internal/translator/openai/claude/openai_claude_response.go", "internal/util/translator.go", "https://github.com/router-for-me/CLIProxyAPI/issues/3769", "commit 9c43abd9"]
links: []
category: debugging
confidence: high
schemaVersion: 1
---

# Claude Tool Use Requires Real Names When OpenAI Streams Omit Function Name

### [Translator] Claude Tool Use Requires Real Names When OpenAI Streams Omit Function Name

**Problem**
OpenAI-compatible streaming providers can emit `tool_calls` deltas with empty or missing `function.name`. Earlier handling kept the `tool_use` block from disappearing by generating synthetic names such as `tool_0`, but Claude Code still cannot execute a tool block whose `name` does not match a tool declared in the original Claude request.

**Cause**
Claude clients match `content_block.type=tool_use` by the client-facing tool `name`. A synthetic fallback preserves the block shape but does not identify an actual client tool. The upstream OpenAI stream may provide arguments and an id while never providing a usable name.

**Impact**
OpenAI-compatible provider responses translated back to Claude can leave Claude Code in repeated tool-call retries or failed tool execution even though `stop_reason` is `tool_use` and the block is present.

**Resolution**
When the upstream omits `function.name`, infer the Claude tool name only from unambiguous original-request evidence: an explicit `tool_choice.type=tool`, exactly one declared tool, or a unique top-level argument-schema match. Keep the existing synthetic fallback only when no real name can be inferred, and cover the real-name cases with translator tests.

**References**
Issue: https://github.com/router-for-me/CLIProxyAPI/issues/3748
Code: `internal/util/translator.go`, `internal/translator/openai/claude/openai_claude_response.go`

---

## Update (2026-06-08T13:33:19.998Z)

### [Translator] Claude Tool Use Requires Real Names When OpenAI Responses Omit Function Name

**Problem**
OpenAI-compatible providers can emit `tool_calls` with empty or missing `function.name`. Streaming responses can drop or defer `tool_use` blocks; non-streaming responses can leak `"name":""` into Anthropic `tool_use` blocks.

**Cause**
Claude clients match `tool_use.name` against the tools declared in the original Claude request. Empty or synthetic-only names do not identify an executable client tool. The upstream OpenAI response may provide arguments and an id while never providing a usable function name.

**Impact**
Claude-compatible clients can enter repeated tool-call retries, fail tool execution, or receive client-visible errors when Anthropic rejects empty tool names. This affects OpenAI-compatible to Claude translation in both streaming and non-streaming paths.

**Resolution**
Use the shared recovery policy before emitting `tool_use`: prefer upstream names mapped through `ToolNameMap`; otherwise infer from unambiguous original Claude request evidence (`tool_choice.type=tool`, exactly one declared tool, or a unique top-level argument-schema match); otherwise fall back to `tool_<idx>` only when arguments exist. Skip blocks that have neither a usable name nor arguments. Non-streaming coverage was added in `9c43abd9` for direct non-stream conversion and `stream:false` dispatch.

**References**
Issues: https://github.com/router-for-me/CLIProxyAPI/issues/3748, https://github.com/router-for-me/CLIProxyAPI/issues/3769
Code: `internal/translator/openai/claude/openai_claude_response.go`, `internal/util/translator.go`
Commit: `9c43abd9`
