# Cursor ApplyPatch Custom Tool Round-Trip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve Codex custom tool calls through Chat Completions streaming/non-streaming responses and reconstruct them safely in the next request so Cursor ApplyPatch can complete its agent loop.

**Architecture:** Build one request-local tool catalog for family classification and name shortening/restoration. Replace the response translator's single active function-call flags with response-stream-local per-item state keyed by `item_id` and `output_index`, and use terminal events only to fill bytes not already emitted.

**Tech Stack:** Go 1.26+, `gjson`, `sjson`, Go `testing`, `httptest`, Gorilla WebSocket, existing SDK translator registry.

## Global Constraints

- Use the current `main` baseline and do not copy PR #4079.
- Add and run failing regression tests before production-code changes.
- Never use process-global or cross-request state for tool-family classification.
- Preserve existing function-call behavior and use one translator for HTTP and WebSocket executors.
- Keep changes small, comments in English, and format all Go changes with `gofmt -w .`.
- Do not add network timeouts after an upstream connection is established.

---

### Task 1: Capture the Root Cause with Failing Translator Tests

**Files:**
- Create: `internal/translator/codex/openai/chat-completions/codex_openai_custom_tool_test.go`
- Modify: `internal/translator/codex/openai/chat-completions/codex_openai_request_test.go:807-1072`

**Interfaces:**
- Consumes: `ConvertCodexResponseToOpenAI`, `ConvertCodexResponseToOpenAINonStream`, and `ConvertOpenAIRequestToCodex`.
- Produces: regression helpers `translateCodexStreamEvents`, `collectChatToolCalls`, and `assertToolInputExactlyOnce` used only by the new test file.

- [ ] **Step 1: Add an end-to-end ApplyPatch transcript test**

Create a test that declares `{"type":"custom","name":"ApplyPatch"}`, feeds added/delta/done/output-item-done/completed events, assembles the Chat `type: "function"` envelope, submits it with a `role: "tool"` result, and verifies the next Codex input contains:

```go
wantCall := map[string]string{
	"type":    "custom_tool_call",
	"call_id": "call_apply_patch",
	"name":    "ApplyPatch",
	"input":   "*** Begin Patch\n*** Add File: cursor-round-trip.txt\n+ok\n*** End Patch",
}
wantOutputType := "custom_tool_call_output"
```

Then translate a final `response.output_text.delta` plus `response.completed` and assert the final assistant text and `finish_reason: "stop"` are still emitted.

- [ ] **Step 2: Add streaming exactly-once and fallback table tests**

Use a table with complete event slices for these cases:

```go
tests := []struct {
	name   string
	events []string
}{
	{name: "multiple deltas then done", events: addedDeltaDeltaDoneItemDoneCompleted},
	{name: "done fallback without deltas", events: addedDoneItemDoneCompleted},
	{name: "output item done fallback", events: addedDeltaItemDoneCompleted},
	{name: "missing added buffers until item done", events: deltaDoneItemDoneCompleted},
	{name: "completed only fallback", events: completedOnly},
}
```

For every row concatenate only `choices.0.delta.tool_calls.*.function.arguments` and require byte-for-byte equality with the patch once, plus one call ID/name announcement and terminal `finish_reason: "tool_calls"`.

- [ ] **Step 3: Add sequential, parallel, non-streaming, and shortening tests**

Cover two sequential custom calls, interleaved custom/function calls with different `item_id` and `output_index`, a completed non-stream custom call, and a custom tool name longer than 64 bytes. Assert tool-call indexes are contiguous and each restored Chat name equals the original declaration.

- [ ] **Step 4: Tighten request history safety tests**

Change the existing missing-output-ID expectation so an ID-less result is accepted only when one pending call remains. Add cases proving:

```go
// Standard Chat envelope classified by this request's declarations.
{"id":"call_custom","type":"function","function":{"name":"ApplyPatch","arguments":"raw patch"}}

// Same envelope remains a function when declaration is function or absent.
{"id":"call_function","type":"function","function":{"name":"lookup","arguments":"{}"}}
```

Also assert duplicate IDs, orphan IDs, ambiguous ID-less mixed outputs, and duplicate results never become `custom_tool_call_output`; a single missing assistant call ID receives one deterministic synthetic ID shared by call and output.

- [ ] **Step 5: Run the new tests and preserve red-state evidence**

Run:

```bash
go test -count=1 -run 'TestApplyPatch|TestCustomTool|TestMixedParallel|TestNonStreamCustom|TestCustomName|TestToolCallOutput' ./internal/translator/codex/openai/chat-completions
```

Expected: FAIL because the current response translator drops `custom_tool_call`, and standard Chat function envelopes are restored as `function_call`/`function_call_output` even when the same request declares a custom tool.

- [ ] **Step 6: Commit the failing regression tests**

```bash
git add internal/translator/codex/openai/chat-completions/codex_openai_custom_tool_test.go internal/translator/codex/openai/chat-completions/codex_openai_request_test.go
git commit -m "test(translator): reproduce Cursor ApplyPatch round-trip failure"
```

---

### Task 2: Add the Request-Local Tool Catalog and Follow-Up Restoration

**Files:**
- Create: `internal/translator/codex/openai/chat-completions/codex_openai_tool_catalog.go`
- Modify: `internal/translator/codex/openai/chat-completions/codex_openai_request.go:69-336`
- Modify: `internal/translator/codex/openai/chat-completions/codex_openai_response.go:522-550`
- Test: `internal/translator/codex/openai/chat-completions/codex_openai_request_test.go`
- Test: `internal/translator/codex/openai/chat-completions/codex_openai_custom_tool_test.go`

**Interfaces:**
- Produces: `buildToolCatalog(raw []byte) toolCatalog`, `toolCatalog.shorten(name string) string`, `toolCatalog.restore(name string) string`, and `toolCatalog.familyForChatCall(name string) toolFamily`.
- Consumes: existing `buildShortNameMap` and `shortenNameIfNeeded`.

- [ ] **Step 1: Implement catalog types and deterministic mappings**

Add:

```go
type toolFamily uint8

const (
	toolFamilyFunction toolFamily = iota
	toolFamilyCustom
)

type toolCatalog struct {
	shortByOriginal  map[string]string
	originalByShort  map[string]string
	customNames      map[string]struct{}
	ambiguousNames   map[string]struct{}
}
```

`buildToolCatalog` must collect `tools.*.function.name` for functions and `tools.*.name` for custom tools, deduplicate names before calling `buildShortNameMap`, mark any name declared by both families ambiguous, and recognize both original and shortened custom names. `familyForChatCall` returns custom only for a unique custom name.

- [ ] **Step 2: Use the catalog for declarations and history**

Replace `originalToolNameMap` with `catalog := buildToolCatalog(rawJSON)`. Shorten names in both function and custom declarations. For assistant history, resolve a standard `type: "function"` call as follows:

```go
family := toolFamilyFunction
name := tc.Get("function.name").String()
input := tc.Get("function.arguments").String()
if catalog.familyForChatCall(name) == toolFamilyCustom {
	family = toolFamilyCustom
}
```

Emit `custom_tool_call` with `input` for custom and the existing `function_call` with `arguments` otherwise. Preserve explicit legacy `type: "custom"` envelopes.

- [ ] **Step 3: Make output matching unique and family-safe**

For explicit output IDs, select exactly one unconsumed pending call. For a missing output ID, build the unconsumed candidate list and match only when its length is one:

```go
if toolCallID == "" {
	if len(candidates) != 1 {
		continue
	}
	pendingIndex = candidates[0]
}
```

Continue dropping duplicate assistant IDs, orphan results, and duplicate results. Select output type solely from the matched pending call's resolved family.

- [ ] **Step 4: Route response name restoration through the same catalog**

Change `buildReverseMapFromOriginalOpenAI` to delegate to `buildToolCatalog(original).originalByShort`, so custom and function calls use identical restoration without global state.

- [ ] **Step 5: Run request and shortening tests**

Run:

```bash
go test -count=1 -run 'Test.*(History|CallID|Output|NameShortening|ApplyPatch).*' ./internal/translator/codex/openai/chat-completions
```

Expected: request-follow-up, ambiguity, and shortening tests PASS; response-event tests may remain FAIL until Task 3.

- [ ] **Step 6: Commit request restoration**

```bash
git add internal/translator/codex/openai/chat-completions/codex_openai_tool_catalog.go internal/translator/codex/openai/chat-completions/codex_openai_request.go internal/translator/codex/openai/chat-completions/codex_openai_response.go internal/translator/codex/openai/chat-completions/*_test.go
git commit -m "fix(translator): restore request-local custom tool history"
```

---

### Task 3: Implement Per-Call Streaming and Non-Streaming Translation

**Files:**
- Create: `internal/translator/codex/openai/chat-completions/codex_openai_tool_stream.go`
- Modify: `internal/translator/codex/openai/chat-completions/codex_openai_response.go:23-314`
- Modify: `internal/translator/codex/openai/chat-completions/codex_openai_response.go:382-519`
- Test: `internal/translator/codex/openai/chat-completions/codex_openai_response_test.go`
- Test: `internal/translator/codex/openai/chat-completions/codex_openai_custom_tool_test.go`

**Interfaces:**
- Produces: `toolCallStreamState`, `streamToolCallTracker`, `findOrCreateToolCall`, `announceToolCall`, `emitToolInput`, and `remainingToolInput`.
- Consumes: `toolCatalog.restore` and the existing Chat completion chunk template.

- [ ] **Step 1: Add per-call stream state**

Define:

```go
type toolCallStreamState struct {
	chatIndex     int
	itemID        string
	outputIndex   int64
	hasOutputIndex bool
	callID        string
	name          string
	family        toolFamily
	announced     bool
	emittedInput  string
	bufferedInput string
}

type streamToolCallTracker struct {
	nextChatIndex int
	byItemID      map[string]*toolCallStreamState
	byOutputIndex map[int64]*toolCallStreamState
	ordered       []*toolCallStreamState
}
```

Store a tracker inside `ConvertCliToOpenAIParams`, initialize it per converter `param`, and remove the single-call `FunctionCallIndex`, `HasReceivedArgumentsDelta`, and `HasToolCallAnnounced` assumptions.

- [ ] **Step 2: Implement identity resolution and exactly-once suffix logic**

Use `item_id` first and `output_index` second. When neither exists, use a call only if exactly one compatible active state exists. Add:

```go
func remainingToolInput(emitted, complete string) (string, bool) {
	if emitted == "" {
		return complete, true
	}
	if complete == emitted {
		return "", true
	}
	if strings.HasPrefix(complete, emitted) {
		return complete[len(emitted):], true
	}
	return "", false
}
```

Never emit a conflicting full value after deltas already reached the client.

- [ ] **Step 3: Translate added, delta, done, and output-item-done events**

Handle both families:

```go
case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
```

Function events read `arguments`; custom events read `input`. `response.output_item.added` announces `function_call` and `custom_tool_call` as Chat `type: "function"`. If added was omitted, buffer delta/done data until `response.output_item.done` supplies `call_id` and `name`, then emit one envelope containing the full remaining input.

- [ ] **Step 4: Add completed-event fallback before the terminal chunk**

On `response.completed`, scan `response.output` for function/custom calls in output order. Reconcile each with the tracker, append any missing announcement/input chunks, then append the terminal chunk. A completed response with any translated call uses:

```go
finishReason := "tool_calls"
nativeFinishReason := "tool_calls"
```

Continue mapping incomplete responses to `length` or `content_filter` without overwriting them merely because a call was previously announced.

- [ ] **Step 5: Add non-stream custom calls**

Extend the non-stream output switch:

```go
case "function_call", "custom_tool_call":
	argumentsPath := "arguments"
	if outputType == "custom_tool_call" {
		argumentsPath = "input"
	}
```

Build the same Chat function envelope, restore the original name, preserve `call_id`, and let the existing `len(toolCalls) > 0` terminal logic return `tool_calls`.

- [ ] **Step 6: Run all translator tests**

Run:

```bash
gofmt -w internal/translator/codex/openai/chat-completions
go test -count=1 ./internal/translator/codex/openai/chat-completions/...
```

Expected: PASS, including exact input equality for all event permutations and the existing function-call/image/usage regressions.

- [ ] **Step 7: Commit response translation**

```bash
git add internal/translator/codex/openai/chat-completions
git commit -m "fix(translator): stream Codex custom tool calls exactly once"
```

---

### Task 4: Prove HTTP and WebSocket Executor Integration

**Files:**
- Create: `internal/runtime/executor/codex_custom_tool_translation_test.go`

**Interfaces:**
- Consumes: `CodexExecutor.ExecuteStream`, `CodexWebsocketsExecutor.ExecuteStream`, and the translator registered by `internal/translator/codex/openai/chat-completions/init.go`.
- Produces: transport-level proof that both executors return the same Chat tool call and terminal reason.

- [ ] **Step 1: Add HTTP SSE integration test**

Use `httptest.NewServer` to emit a custom added event, two input deltas, done, output-item-done, and completed. Call the HTTP executor with:

```go
opts := cliproxyexecutor.Options{
	SourceFormat: sdktranslator.FromString("openai"),
	Stream:       true,
}
```

Collect downstream SSE payloads and assert `call_apply_patch`, `ApplyPatch`, the exact patch once, and `finish_reason: "tool_calls"`.

- [ ] **Step 2: Add WebSocket integration test with the same assertions**

Upgrade an `httptest` server using Gorilla WebSocket, read the `response.create` request, send the same JSON events as text frames, and call `CodexWebsocketsExecutor.ExecuteStream` with the same Chat payload/options. Reuse only assertion helpers; do not duplicate translator logic in the test.

- [ ] **Step 3: Run both executor tests and the translator suite**

Run:

```bash
go test -count=1 -run 'TestCodex.*CustomTool' ./internal/runtime/executor
go test -count=1 ./internal/translator/codex/openai/chat-completions/...
```

Expected: PASS for both transports with identical call metadata, input, and finish reason.

- [ ] **Step 4: Commit executor coverage**

```bash
git add internal/runtime/executor/codex_custom_tool_translation_test.go
git commit -m "test(executor): cover custom tools over HTTP and WebSocket"
```

---

### Task 5: Full Verification and Requirement Audit

**Files:**
- Modify only files found defective by verification, using a new failing test before any corrective production change.

**Interfaces:**
- Consumes: the complete repository and required build target.
- Produces: fresh command output proving formatting, targeted behavior, full regressions, and compilation.

- [ ] **Step 1: Format and inspect the final diff**

```bash
gofmt -w .
git diff --check
git status --short
git diff main...HEAD --stat
```

Expected: no formatting errors, only in-scope files, and no `test-output` artifact.

- [ ] **Step 2: Run the exact requested validations**

```bash
go test ./internal/translator/codex/openai/chat-completions/...
go test ./...
go build -o test-output ./cmd/server && rm test-output
```

Expected: all three commands exit 0 and `test-output` no longer exists.

- [ ] **Step 3: Audit every explicit requirement against test names and source**

Use `rg` to confirm all five custom event types, request-local catalog construction, custom output conversion, non-stream mapping, name restoration, and both executor tests are present. Record any missing evidence as incomplete and add a failing regression before correcting it.

- [ ] **Step 4: Commit a corrective regression only if the audit found a gap**

Return to the applicable earlier task, add a failing test, make the minimal corrective change, rerun all Task 5 commands, and commit the exact files changed by that correction with `git commit -m "test(translator): complete custom tool regression coverage"`. Skip this step when the audit found no gap.

---

### Task 6: Publish the Draft PR

**Files:**
- No source changes expected.

**Interfaces:**
- Consumes: clean `codex/fix-cursor-apply-patch` with all validations passing.
- Produces: origin branch and draft PR targeting `router-for-me/CLIProxyAPI:dev`.

- [ ] **Step 1: Confirm publication scope**

```bash
git status --short --branch
git log --oneline main..HEAD
git remote -v
```

Expected: clean branch, only intentional commits, and `origin` points to `DOUIF/CLIProxyAPI`.

- [ ] **Step 2: Push the branch**

```bash
git push -u origin codex/fix-cursor-apply-patch
```

Expected: remote tracking branch created or updated successfully.

- [ ] **Step 3: Open a cross-repository draft PR**

Use `gh pr create --repo router-for-me/CLIProxyAPI --base dev --head DOUIF:codex/fix-cursor-apply-patch --draft` with a body covering root cause, response/request changes, tests, and exact validation commands.

Expected: one draft PR URL targeting upstream `dev`.

---

### Task 7: Run Cursor SSH Live Verification and Clean Up

**Files:**
- Create remotely and dispose: one dedicated ApplyPatch verification file outside tracked project files or remove it before restoring the checkout.

**Interfaces:**
- Consumes: the open Cursor window connected to `SSH: tufa15`, the pushed branch, existing CPA configuration/auth, and GPT-5.6 Sol Medium.
- Produces: visible ApplyPatch execution, final assistant continuation, CPA trace containing `custom_tool_call_output`, and a clean restored remote checkout.

- [ ] **Step 1: Record remote pre-test state in Cursor**

In the existing terminal record the current branch, `git status --short`, and any running CPA process. Do not overwrite unrelated remote changes; stop and report them if the checkout is not clean.

- [ ] **Step 2: Fetch, check out, build, and start the test CPA**

Fetch origin, check out `codex/fix-cursor-apply-patch`, build `./cmd/server`, and start CPA with the existing remote configuration while retaining trace output. Preserve the prior process command so it can be restored if needed.

- [ ] **Step 3: Run the Cursor agent prompt**

Select GPT-5.6 Sol with Medium reasoning and submit:

```text
Use ApplyPatch (not a shell redirection or another file-edit tool) to create or update the dedicated file cursor-apply-patch-round-trip.txt so it contains exactly: APPLY_PATCH_ROUND_TRIP_OK. After the tool succeeds, continue reasoning and give a final answer confirming the file content.
```

Expected: Cursor displays the ApplyPatch tool call/result and then a final assistant answer rather than stopping after the tool.

- [ ] **Step 4: Verify file and trace evidence**

Read the disposable file and search the CPA trace for the matching call ID plus `custom_tool_call_output`. Confirm the upstream follow-up includes the successful tool output and a later final response event.

- [ ] **Step 5: Restore the remote environment**

Stop the test CPA, remove the disposable file, restore the pre-test branch/process state, and run `git status --short`. Expected: the remote checkout is clean and no test CPA process remains.
