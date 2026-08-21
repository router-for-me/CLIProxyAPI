# Cursor ApplyPatch Custom Tool Round-Trip Design

## Context

Cursor sends Codex-backed requests through the OpenAI Chat Completions compatibility endpoint. Codex Responses emits ApplyPatch as a `custom_tool_call`, while Chat Completions represents every client-visible tool call with the standard `type: "function"` envelope. The current response translator only recognizes `function_call` events, and the current request translator only restores the custom family when history already uses a non-standard `type: "custom"` envelope. Consequently the custom call is either omitted from the response or returned upstream as a normal function call/output, interrupting the agent loop.

The fix remains entirely request-scoped or response-stream-scoped. It must not use process-global state or carry tool-family state across requests. HTTP and WebSocket executors continue to use the same registered translator.

## Tool Catalog

Build a catalog from the current Chat Completions request's `tools` array. The catalog records:

- every declared function or custom tool name;
- which names uniquely identify custom tools;
- original-to-shortened and shortened-to-original mappings generated across both families; and
- names that are ambiguous because both families claim the same effective name.

Custom declarations remain Responses-compatible top-level `type: "custom"` objects, but their names use the same deterministic shortening rules as function declarations. A history call is restored as custom only when its name uniquely matches a custom declaration in this request. Ambiguous or unknown names remain function calls.

## Follow-Up Request Translation

Chat assistant history normally contains `type: "function"`, `function.name`, and `function.arguments`, even for a client-visible custom call. For each assistant tool-call batch:

1. Resolve the tool name through the request-local catalog.
2. Emit `custom_tool_call` with the bare `function.arguments` string as `input` when the name uniquely identifies a custom declaration.
3. Otherwise emit the existing `function_call` with `arguments` unchanged.
4. Record the resolved family next to the call ID for matching immediately following `role: "tool"` messages.

Explicit legacy `type: "custom"` history remains supported.

Tool outputs match a unique, unconsumed pending call. An explicit call ID must match exactly. A missing output ID may match only when exactly one pending call remains. Duplicate IDs, orphan outputs, duplicate outputs, and otherwise ambiguous outputs are dropped rather than guessed. The matched family selects `custom_tool_call_output` or `function_call_output`.

Missing assistant call IDs receive deterministic request-local synthetic IDs so a uniquely matched output can preserve a valid pair.

## Streaming Response Translation

Replace the single active-call booleans with per-call stream state held in `ConvertCliToOpenAIParams`. Calls are keyed by `item_id`, with `output_index` as a secondary key. Each state records:

- the allocated contiguous Chat tool-call index;
- item ID, output index, call ID, restored name, and family;
- whether the Chat call envelope was announced;
- input already emitted downstream; and
- buffered input observed before enough metadata exists to announce the call.

For `response.output_item.added`, allocate or recover the call state and emit the Chat `tool_calls` envelope when the item is a function or custom call. Both families appear downstream as `type: "function"`; custom free-form input is carried in `function.arguments`.

For argument/input delta events, emit the delta immediately when the call has been announced. If `added` was omitted and name/call ID are not yet known, buffer the data until a later item event supplies metadata.

For argument/input done events, compare the complete value with the input already emitted. Emit only the un-emitted suffix when the complete value has the emitted value as a prefix. Emit the full value when nothing has been emitted. If the values conflict, do not duplicate already emitted bytes.

For `response.output_item.done`, announce a call omitted from `added`, then apply the same suffix fallback using the item's complete `arguments` or `input`.

For `response.completed`, scan output calls and emit any still-missing envelope or input before the terminal chunk. A completed custom call sets `finish_reason` and `native_finish_reason` to `tool_calls`. This covers providers that omit added, delta, done, or output-item-done events.

Sequential and parallel calls remain independent because each call owns its emitted-input and announcement state. Events lacking both identity fields may use the sole active compatible call; when multiple candidates exist, the event is ignored as ambiguous.

## Non-Streaming Response Translation

Treat `custom_tool_call` output items like function calls when building Chat `message.tool_calls`. Preserve `call_id`, restore the original tool name, place bare `input` in `function.arguments`, and return `finish_reason: "tool_calls"` when at least one tool call is present.

## Regression Strategy

Before implementation, add tests that fail on the current `main` behavior and preserve their failing output as root-cause evidence. Coverage includes:

- the complete ApplyPatch transcript from custom declaration through streamed Chat tool call, follow-up `custom_tool_call_output`, and final assistant continuation;
- multi-delta input with exactly-once concatenation;
- done, output-item-done, and completed fallbacks with omitted preceding events;
- sequential custom calls and mixed parallel custom/function calls;
- streaming and non-streaming name restoration after shortening;
- duplicate, missing, unmatched, and ambiguous call IDs;
- standard function-call regression behavior; and
- HTTP and WebSocket executor paths using the registered Chat Completions translator.

After implementation, run formatting, the targeted translator suite, the complete Go suite, and the required server build command.

## Publication and Live Verification

Commit and push `codex/fix-cursor-apply-patch`, then open a draft pull request against upstream `dev`. In the existing Cursor SSH workspace on `tufa15`, save the prior branch and process state, check out the test branch, build and start CPA, and run GPT-5.6 Sol at medium reasoning with a prompt that explicitly requires ApplyPatch to edit a dedicated disposable file.

Success requires the file edit, a returned tool result, continued model reasoning, and a final assistant answer. CPA trace evidence must show the follow-up `custom_tool_call_output`. Afterwards stop the test CPA process, remove the disposable file, restore the prior remote checkout, and verify a clean worktree.
