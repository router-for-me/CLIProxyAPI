# Claude Code 400 tool-use concurrency error

## Summary

Claude Code requests sent through CLIProxyAPI to the Antigravity Claude path intermittently failed with:

```text
API Error: 400 due to tool use concurrency issues
```

The error was reproducible in a long conversation with multiple tool calls. Affected requests returned HTTP 400 while nearby requests in the same session returned HTTP 200.

## Production evidence

The redacted diagnostic captured one failing request on 2026-09-25:

- Claude CLI: `2.1.282`
- CLIProxyAPI: `7.3.17`, commit `9bdde54`
- Endpoint: `POST /v1/messages?beta=true`
- Translated model: `claude-opus-4-6-thinking`
- Translated payload size: about 276 KB
- Upstream response: HTTP 400, `invalid_request_error`
- Upstream message:

```text
messages.20: tool_use ids were found without tool_result blocks immediately after
```

The translated final turns had this shape:

```text
model: [text, functionCall]
user:  [text, functionResponse]
```

The request was rejected by the upstream validator before a model response was produced. This was not a Nginx timeout, account quota error, or concurrency limit in the executor. A separate `503 MODEL_CAPACITY_EXHAUSTED` event occurred during account failover and is unrelated.

## Root cause

Claude tool results must be immediately adjacent to the model turn containing their corresponding tool calls. The Claude-to-Gemini/Antigravity translator converted a user message containing both normal text and `tool_result` blocks into one Gemini user content turn.

The resulting mixed turn allowed text to sit between the model function call and its function response from the provider's validation perspective. The upstream therefore treated the tool call as missing its immediate result and returned HTTP 400.

This matches the invariant documented in [CLIProxyAPI issue #4112](https://github.com/router-for-me/CLIProxyAPI/issues/4112), which reports the same validation error for stale or non-adjacent `tool_use`/`tool_result` history.

## Fix

The Claude path now splits a Gemini user content turn containing `functionResponse` parts:

1. A user turn containing only `functionResponse` parts is emitted immediately after the model function call.
2. Other text or reminder parts are emitted in a following user turn.
3. Claude uses `MergeAdjacentGeminiUserContents`, which does not merge turns containing function responses.
4. Non-Claude Gemini translation keeps its previous merge behavior.

The change is implemented in:

- `internal/translator/common/gemini.go`
- `internal/translator/antigravity/claude/antigravity_claude_request.go`
- `internal/translator/common/gemini_test.go`

The temporary production diagnostic logger used to capture the evidence was removed before the review commit, so prompts, tool arguments, tool IDs, and tool output are not added to the branch.

## Verification

Targeted tests pass:

```text
go test ./internal/translator/common ./internal/translator/antigravity/claude ./internal/runtime/executor -run 'Test(.*Gemini.*|.*Antigravity.*)' -count=1
```

After deployment, multiple fresh Claude requests returned HTTP 200. No new adjacency diagnostic was observed. The deployed service remained active.

## Scope and limitations

This fix repairs the proxy's translated turn boundary. It does not rewrite or delete the user's Claude Code session history. A conversation that already contains a permanently orphaned tool call may still need `/rewind`, `/clear`, or a new session.

The branch does not include production debug logging. If another 400 appears, diagnostic instrumentation can be reintroduced temporarily, reviewed separately, and removed before a final PR.

## Implementation

The fix is contained in the branch commit titled:

```text
fix(translator): preserve tool result adjacency for Claude models
```
