# Phase 3 — Translator Correctness Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
> **Worktree discipline:** Work directly on `main`. No `git stash`. No standalone `internal/translator/` changes — each task ships alongside non-translator changes (executor/helps, usage tracking).

**Goal:** Fix five translator correctness gaps (C1-C5) identified during upstream release absorption, delivering consistent tool-name capping, per-turn tool-call-ID scoping, JSON Schema normalization, centralized finish-reason mapping, and cache-token consistency. Each fix must ship with non-translator changes to satisfy the AGENTS.md translator constraint.

**Architecture:** Route all tool-name capping through the existing `internal/util` `SanitizedFunctionNameMap`, retire the duplicated Codex `buildShortNameMap`. Add boolean-subschema handling to `cleanJSONSchema`. Centralize the six inline finish-reason switches into `translator/common` helpers and add `finish_reason` to `usage.Record`. Fix `extractResponsesUsage` to handle `cache_creation` tokens and audit the 15+ translator cache-field copy sites.

**Tech Stack:** Go 1.26+, existing `internal/util/`, `internal/translator/`, `internal/runtime/executor/helps/`

**AGENTS.md constraint check:** C4 adds `finish_reason` to `usage.Record` (non-translator, in `internal/runtime/executor/`). C5 fixes `extractResponsesUsage` in `helps/usage_helpers.go` (non-translator). C1 modifies executor response-restore paths. All tasks pass the "broader changes" requirement.

---

## Current State (from exploration, 2026-09-24)

### C1 — Tool-name capping
- `SanitizedFunctionNameMap` + collision hash suffix: `internal/util/translator.go:285` — works, used by Gemini/Antigravity translators
- `RestoreSanitizedToolName`: `internal/util/translator.go:488` — reverse lookup
- Codex translators (`codex_claude_request.go:584`, `codex_gemini_request.go:537`): own `buildShortNameMap` that shortens to `_0`, `_1`, `_2` — completely separate, not collision-aware
- Claude→OpenAI translators: **no capping at all** — tool names pass through verbatim

### C2 — Tool-call-ID scoping
- No cross-turn leakage found in any translator — all maps (`tcID2Name`, `toolNameByID`, `pendingCall`) are rebuilt per-request
- Codex→Gemini has the most elaborate pattern (`pendingCallIDs` queue with `removePendingCallID`), which could be a shared helper
- Design doc overestimated the scope; actual work is pattern extraction only

### C3 — Schema normalization
- `cleanJSONSchema` in `internal/util/gemini_schema.go:62` — handles union flattening, keyword stripping, but **no boolean subschema handling**
- Boolean subschema (`true`/`false` as literal JSON Schema — OpenAPI 3.1): entirely missing
- Entry points are inline in executor files, not a named `sanitizeAntigravityRequestSchemas` function

### C4 — Finish-reason mapping
- 6 inline implementations found, only 2/6 handle `content_filter`:
  - Codex→Claude: maps to `refusal` ✓
  - OpenAI→Gemini: maps to `SAFETY` ✓
  - Others: default to `end_turn`/`stop` ✗
- No `finish_reason` field on `usage.Record` — this is the non-translator half

### C5 — Cache-token consistency
- `billableUncachedInput`: `helps/usage_helpers.go:1076` — correct helper
- `extractResponsesUsage` (codex_claude_response.go:801): only handles `cache_read` (`cached_tokens`), misses `cache_creation` tokens
- Claude→OpenAI adds cache tokens to `prompt_tokens` (correct), Codex→Claude subtracts them from `input_tokens` (symmetric but fragile)

---

## Task Breakdown

### Task 1: C1 — Canonical tool-name capping

**Files:**
- Modify: `internal/util/translator.go` — add `mcp__` prefix-preserve logic to `SanitizedFunctionNameMap` if not already there (verify: the regex `[^a-zA-Z0-9_.:-]` keeps `._:-`, so `mcp__` colons might not be affected; the actual issue is `mcp__` with `/` — check if `functionNamesFromRequest` handles `mcp__` patterns correctly)
- Modify: `internal/util/util.go` — verify `SanitizeFunction` preserves `mcp__` prefix (regex `[^a-zA-Z0-9_.:-]` — `_` and `:` allowed, but `/` is not; if tool name is `mcp__server_read` it's fine, if `mcp/server/read` the `/` gets replaced by `_` — confirm this matches Claude's actual tool naming)
- Modify: `internal/translator/codex/claude/codex_claude_request.go:530-606` — replace `buildShortNameMap` with `util.SanitizedFunctionNameMap`/`util.MapSanitizedFunction`
- Modify: `internal/translator/codex/gemini/codex_gemini_request.go:537-570` — same replacement
- Modify: `internal/translator/claude/openai/chat-completions/claude_openai_request.go` — add tool-name capping using `util.SanitizedFunctionNameMap` (currently passes names through verbatim)
- Modify: `internal/translator/claude/openai/responses/claude_openai-responses_request.go` — same capping addition
- Modify: executor files that call `RestoreSanitizedToolName` / use reverse maps (verify each translator's response path calls restore; add where missing)

**Step 1: Write failing test — Codex tool-name mapping uses canonical path**

Add test in `internal/util/sanitize_test.go` (or a new test file) proving that a tool name `my-long-function-name-that-exceeds-64-chars-with-some-unique-suffix` produces the same sanitized result whether routed through `SanitizedFunctionNameMap` or `buildShortNameMap` (i.e., they are equivalent), or better, add a test that the codex translator produces the same tool names when `buildShortNameMap` is replaced.

Add test in `internal/translator/claude/openai/*_test.go` proving that a tool name with `/` or `:` characters gets capped to 64 chars after passing through `MapSanitizedFunction`.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/util/ -run TestC1ToolNameCanonical -v`
Expected: FAIL (test for codex equivalence fails or test for claude-openai capping fails — whichever you wrote first)

**Step 3: Implement changes**

```go
// In codex_claude_request.go — replace buildShortNameMap usage:
// BEFORE:
// m := buildShortNameMap(names)
// AFTER:
nameMap := util.SanitizedFunctionNameMap(rawJSON)
mapped := util.MapSanitizedFunction(nameMap, name)

// In claude_openai_request.go — add capping to tool definitions:
for _, tool := range request.Tools {
    tool.Function.Name = util.MapSanitizedFunction(nameMap, tool.Function.Name)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/util/ -run TestC1ToolNameCanonical -v`
Expected: PASS

**Step 5: Run broader tests**

Run: `go test ./internal/translator/... ./internal/util/...`
Expected: All green (no regressions from replacing buildShortNameMap)

**Step 6: Commit**

```bash
git add internal/util/ internal/translator/codex/ internal/translator/claude/
git commit -m "feat(translator): canonical tool-name capping via SanitizedFunctionNameMap

Retire Codex buildShortNameMap, add capping to Claude->OpenAI translators.
All tool names now route through the single collision-aware path."
```

---

### Task 2: C3 — Boolean subschema handling in cleanJSONSchema

**Files:**
- Modify: `internal/util/gemini_schema.go` — add boolean-subschema normalization to `cleanJSONSchema`
- Modify: `internal/util/gemini_schema_test.go` — test boolean subschema cases

**Step 1: Write the failing test**

Add test cases proving that a schema like `{"type": "boolean"}` or a boolean subschema `true` (which in JSON Schema 2020-12 means "any valid value") is normalized correctly. Also test `{"then": {"type": "boolean"}, "else": {...}}` conditional subschemas with boolean values.

```go
func TestCleanJSONSchemaHandlesBooleanSubschemas(t *testing.T) {
    // A standalone boolean true as schema (accept everything) should become
    // an empty object placeholder or be handled gracefully.
    // A boolean false as schema (reject everything) should become
    // {"not": {"type": "object"}} or a rejected-with-description pattern.
    
    input := []byte(`{"properties": {"active": {"type": "boolean"}}}`)
    // This is a normal boolean FIELD type, not a boolean subschema.
    // The problematic case is when the schema VALUE itself is true/false:
    // {"items": true} or {"additionalProperties": false}
    
    // True subschema:
    trueSub := []byte(`{"items": true}`)
    cleaned, err := CleanJSONSchemaForAntigravity(trueSub)
    // Should not crash or return empty
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/util/ -run TestCleanJSONSchemaHandlesBooleanSubschemas -v`
Expected: FAIL with panic or wrong output (current code doesn't handle `true`/`false` subschema values)

**Step 3: Implement changes**

In `cleanJSONSchema` (gemini_schema.go), before or during the keyword-removal phase, add:
```go
// Handle boolean subschemas (true = permissive, false = rejecting).
if json.Valid(raw) {
    // A top-level boolean value as schema
    if bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
        return []byte("{}"), nil  // permissive empty schema
    }
    if bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
        return []byte(`{"not": {"type": "object"}}`), nil
    }
}
// For nested boolean subschemas, walk the JSON and replace true→{}, false→{"not": {"type": "object"}}
```

Also ensure `removePlaceholderFields` doesn't strip the added `not` schema.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/util/ -run TestCleanJSONSchema -v`
Expected: All PASS (including existing tests)

**Step 5: Commit**

```bash
git add internal/util/gemini_schema.go internal/util/gemini_schema_test.go
git commit -m "feat(util): boolean subschema handling in cleanJSONSchema

Normalize literal true/false JSON Schema values: true becomes empty
schema (permissive), false becomes rejecting not-schema."
```

---

### Task 3: C4 — Centralized finish-reason mapping + usage record

**Non-translator component** (AGENTS.md anchor): add `FinishReason` to `usage.Record` and wire it through the executor usage-collection paths. This satisfies the "broader changes" requirement.

**Files:**
- Create: `internal/translator/common/finish_reason.go` — shared mapping helpers
- Create: `internal/translator/common/finish_reason_test.go`
- Modify: All 6 translator response files to use the shared helpers (see list below)
- Modify: `internal/usage/` — add `FinishReason` field to `Record`
- Modify: `internal/runtime/executor/helps/usage_helpers.go` — wire FinishReason capture

**Step 1: Write the failing test**

In `internal/translator/common/finish_reason_test.go`:
```go
func TestMapClaudeStopReasonToOpenAI(t *testing.T) {
    tests := []struct{ in, want string }{
        {"end_turn", "stop"},
        {"tool_use", "tool_calls"},
        {"max_tokens", "length"},
        {"stop_sequence", "stop"},
        {"content_filter", "stop"},    // OpenAI has no content_filter in chat
        {"unknown_reason", "stop"},    // fallback
    }
    for _, tc := range tests {
        got := ClaudeStopReasonToOpenAIFinishReason(tc.in)
        if got != tc.want {
            t.Errorf(...)
        }
    }
}

func TestMapOpenAIFinishReasonToClaudeStopReason(t *testing.T) {
    tests := []struct{ in string; hasToolCall bool; want string }{
        {"stop", false, "end_turn"},
        {"stop", true, "tool_use"},
        {"length", false, "max_tokens"},
        {"content_filter", false, "refusal"},
        {"content_filter", true, "refusal"},
    }
    ...
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/translator/common/ -run TestMapClaudeStopReason -v`
Expected: FAIL with "function not defined"

**Step 3: Implement `finish_reason.go`**

```go
// Package common provides shared translator helpers.
package common

// ClaudeStopReasonToOpenAIFinishReason maps Claude stop_reason → OpenAI finish_reason.
func ClaudeStopReasonToOpenAIFinishReason(reason string) string {
    switch reason {
    case "end_turn":      return "stop"
    case "tool_use":      return "tool_calls"
    case "max_tokens":    return "length"
    case "stop_sequence": return "stop"
    default:              return "stop"
    }
}

// ClaudeStopReasonToGeminiFinishReason maps Claude stop_reason → Gemini finish_reason.
func ClaudeStopReasonToGeminiFinishReason(reason string) string {
    switch reason {
    case "end_turn":      return "STOP"
    case "tool_use":      return "STOP"
    case "max_tokens":    return "MAX_TOKENS"
    case "stop_sequence": return "STOP"
    default:              return "STOP"
    }
}

// GeminiFinishReasonToClaudeStopReason maps Gemini finish_reason → Claude stop_reason.
func GeminiFinishReasonToClaudeStopReason(reason string, hasToolCall bool) string {
    if hasToolCall { return "tool_use" }
    switch reason {
    case "STOP", "FINISH_REASON_UNSPECIFIED", "": return "end_turn"
    case "MAX_TOKENS":                          return "max_tokens"
    case "SAFETY":                              return "refusal"
    default:                                    return "end_turn"
    }
}

// OpenAIFinishReasonToClaudeStopReason maps OpenAI finish_reason → Claude stop_reason.
func OpenAIFinishReasonToClaudeStopReason(reason string, hasToolCall bool) string {
    if hasToolCall { return "tool_use" }
    switch reason {
    case "stop":            return "end_turn"
    case "length":          return "max_tokens"
    case "content_filter":  return "refusal"
    default:                return "end_turn"
    }
}
```

**Step 4: Wire the 6 translator response files**

Retire inline switches in:
1. `internal/translator/codex/claude/codex_claude_response.go:473-507` → `common.OpenAIFinishReasonToClaudeStopReason`
2. `internal/translator/antigravity/claude/antigravity_claude_response.go:509-522` → `common.GeminiFinishReasonToClaudeStopReason`
3. `internal/translator/claude/openai/chat-completions/claude_openai_response.go:278-289` → `common.ClaudeStopReasonToOpenAIFinishReason`
4. `internal/translator/openai/gemini/openai_gemini_response.go:246-259` → `common.OpenAIFinishReasonToGeminiFinishReason`
5. `internal/translator/antigravity/openai/chat-completions/antigravity_openai_response.go:210-220` → `common.ClaudeStopReasonToOpenAIFinishReason` (or whichever direction applies)
6. `internal/translator/gemini/openai/chat-completions/gemini_openai_response.go:253+` → shared helper

**Step 5: Add FinishReason to usage.Record**

In `internal/usage/`, find the `Record` struct and add:
```go
FinishReason string `json:"finish_reason,omitempty"`
```

In the executor usage-collection paths (e.g. where `usage_helpers.go` builds the `Detail` struct), capture the upstream `finish_reason`/`stop_reason` from the response before any mapping. This is the **non-translator change** that satisfies AGENTS.md.

**Step 6: Run tests**

Run: `go test ./internal/translator/... ./internal/usage/... ./internal/runtime/executor/helps/`
Expected: All green

**Step 7: Commit**

```bash
git add internal/translator/common/finish_reason.go internal/translator/common/finish_reason_test.go \
       internal/translator/codex/ internal/translator/antigravity/ \
       internal/translator/claude/ internal/translator/openai/ internal/translator/gemini/ \
       internal/usage/ internal/runtime/executor/helps/
git commit -m "feat(translator): centralized finish-reason mapping + usage record

Add FinishReason to usage.Record. Centralize 6 inline finish-reason switches
into translator/common with explicit content_filter handling."
```

---

### Task 4: C5 — Cache-token consistency

**Non-translator component:** fix `extractResponsesUsage` and `billableUncachedInput` callers.

**Files:**
- Modify: `internal/translator/codex/claude/codex_claude_response.go` — fix `extractResponsesUsage` to handle `cache_creation` tokens
- Modify: `internal/runtime/executor/helps/usage_helpers.go` — audit `billableUncachedInput` callers for double-counting
- Modify: `internal/usage/` — verify `CacheCreationTokens` field is correctly populated by all paths
- Test files for each modified package

**Step 1: Write failing test**

Test proving that `extractResponsesUsage` drops `cache_creation` tokens:
```go
func TestExtractResponsesUsageHandlesCacheCreation(t *testing.T) {
    // Build a Responses usage JSON with both cached_tokens and cache_creation tokens
    // Assert that the output Detail has CacheCreationTokens set correctly
}
```

**Step 2: Implement fix for extractResponsesUsage**

In the codex→claude response path, ensure `cache_creation_input_tokens` from OpenAI Responses usage is read and written into the correct Claude usage field.

**Step 3: Audit billableUncachedInput callers**

Check each site in `usage_helpers.go` where `billableUncachedInput` is called (lines 822, 1015, 1064) to ensure the input values (`inputTotal`, `cacheRead`, `cacheWrite`) are correct and not double-counted.

**Step 4: Run tests**

Run: `go test ./internal/translator/codex/... ./internal/runtime/executor/helps/ ./internal/usage/`
Expected: All green

**Step 5: Commit**

```bash
git add internal/translator/codex/ internal/runtime/executor/helps/ internal/usage/
git commit -m "fix(translator): handle cache_creation tokens in extractResponsesUsage

extractResponsesUsage now reads cache_creation tokens from Responses usage.
Audited billableUncachedInput callers for double-counting."
```

---

### Task 5: C2 — Per-turn tool-call-ID scoping pattern (low priority)

**Rationale:** No cross-turn leakage bugs found. This task extracts the Codex→Gemini `pendingCallIDs` queue pattern into `translator/common` for consistency, but does NOT fix any live bug. If time is tight, defer to a later cleanup round.

**Files:**
- Create: `internal/translator/common/tool_call_ids.go` — shared `PendingToolCallIDs` helper
- Create: `internal/translator/common/tool_call_ids_test.go`
- Modify: `internal/translator/codex/gemini/codex_gemini_request.go:72-221` — use shared helper

**Step 1: Write test**

```go
func TestPendingToolCallIDs(t *testing.T) {
    ids := NewPendingToolCallIDs()
    ids.Add("call1", "call2")
    if ids.Len() != 2 { t.Fatal("expected 2 pending") }
    id := ids.Consume("call1")
    if id != "call1" { t.Fatal("consume should return matching call") }
    id = ids.Consume("nonexistent")
    if id != "" { t.Fatal("consume should return empty for missing") }
}
```

**Step 2: Implement shared helper**

```go
type PendingToolCallIDs struct {
    ids []string
}

func (p *PendingToolCallIDs) Add(newIDs ...string) {
    p.ids = append(p.ids, newIDs...)
}

func (p *PendingToolCallIDs) Consume(id string) string {
    for i, pending := range p.ids {
        if pending == id {
            p.ids = append(p.ids[:i], p.ids[i+1:]...)
            return id
        }
    }
    if len(p.ids) > 0 {
        id = p.ids[0]
        p.ids = p.ids[1:]
    }
    return id
}
```

**Step 3: Wire into codex_gemini_request.go**

Replace the inline `pendingCallIDs` slice + `removePendingCallID` + queue-head fallback with the shared helper.

**Step 4: Test**

Run: `go test ./internal/translator/common/ ./internal/translator/codex/`
Expected: All green

**Step 5: Commit**

```bash
git add internal/translator/common/tool_call_ids.go internal/translator/common/tool_call_ids_test.go \
       internal/translator/codex/gemini/codex_gemini_request.go
git commit -m "feat(translator): shared pending-tool-call-IDs helper

Extract Codex->Gemini per-turn tool-call-ID queue pattern into
translator/common/PendingToolCallIDs for reuse."
```

---

## Definition of Done

- `gofmt -w .` clean
- `go build -o test-output ./cmd/server && rm test-output` succeeds
- `go test ./internal/translator/...` green
- `go test ./internal/util/...` green
- `go test ./internal/usage/...` green
- `go test ./internal/runtime/executor/helps/` green
- All touched translator files have non-translator companions in the same commit
- Every commit message ends with `Co-Authored-By: Claude Code <noreply@anthropic.com>`
- Docs/plans force-added (`git add -f`) — `docs/*` is gitignored
- No `log.Fatal`/`log.Fatalf`, no timeouts after upstream connection, English comments
- Final tag: `v7.2.138-0.1.30` (bump patch from Phase 2's `v7.2.138-0.1.29`)
