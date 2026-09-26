# Codex Tool Search Shim

This Go dynamic-library plugin adapts the Codex client-executed `tool_search`
protocol for routes that do not expose OpenAI Responses upstream, and it repairs
selected native Responses tool declarations for upstreams with stricter schema
validation.

The plugin addresses context growth without deleting unrelated request data:

- eager tools stay present and are counted against the active-tool byte budget;
- only tools explicitly marked `defer_loading: true` are removed after a
  Responses client advertises a search contract;
- a flat `tool_search_output` activates only the returned tools;
- activated declarations use deterministic aliases, while response translation
  restores the original names and namespaces;
- discovery history is bounded by `max_active_tool_bytes`, newest round first.

The byte budget is a serialization limit, not a token-count guarantee. This
repository's integration test measured 500 deferred tool declarations shrinking
from 1,009,807 bytes before the plugin to 33,819 bytes after it. A separate
real-provider A/B used the same 100 deferred schemas, prompt, model, and output
limit through Antigravity OAuth: `input_tokens` changed from `19,660` to `612`
(`19,048`, `96.8871%`) while both responses completed with `output_tokens=5`.
This is a workload-specific measurement, not a universal token guarantee.

## Modes and protocol gates

There are two independent modes:

- **Bridge mode** applies to Responses-family clients when the selected
  upstream is not Responses. The plugin converts Codex search declarations and
  history to ordinary function tools, then converts upstream calls back into
  `tool_search_call` and namespaced Responses items.
- **Strict native mode** applies only to models explicitly listed in
  `strict_responses_models`. It normalizes selected `tool_search` schemas and
  local schema references without converting the route to ordinary function
  tools.

`bridge_native_models` is a third, independent switch. It makes an explicitly
listed native Responses route use ordinary-function bridging. It does not
activate strict schema normalization, and `strict_responses_models` does not
activate native bridging.

Only Responses-family clients are bridge candidates. Built-in source formats are
`openai-response`, `openai-responses`, `responses`, `response`, and `codex`;
additional identifiers can be configured through `extra_source_formats`.
Requests and responses from other client protocols are not rewritten.

The Codex model catalog is changed only for model-list responses carrying the
host's trusted `codex_client_models` metadata and only for exact entries in
`bridge_models`. With the default empty list, the plugin publishes no
`supports_search_tool` capability.

## Configuration

```yaml
plugins:
  configs:
    codex-tool-search-shim:
      enabled: true
      priority: 100
      strict_responses_models: []
      bridge_models: []
      bridge_native_models: []
      extra_source_formats: []
      diagnostics_log_path: ""
      max_active_tool_bytes: 262144
      max_request_states: 512
      max_state_bytes: 33554432
      max_schema_expansion_bytes: 65536
      max_schema_expansion_nodes: 10000
      max_schema_depth: 64
      strip_custom_tools: false
```

### Model selection

- `strict_responses_models` accepts exact names or `*` wildcards for strict
  native schema repairs.
- `bridge_models` is empty by default. Entries are exact, case-insensitive
  Codex model slugs; only matching catalog entries receive
  `supports_search_tool: true`.
- `bridge_native_models` is empty by default. Entries are exact,
  case-insensitive model identifiers.

Matching a bridge model is an explicit compatibility decision because the
upstream must implement the ordinary-function search contract used by this
plugin.

### Budgets and rejection behavior

- `max_active_tool_bytes` counts the serialized `tools` and `additional_tools`
  arrays. Eager declarations that do not fit return HTTP 413 with
  `active_tool_budget_exceeded`; they are never silently dropped.
- Discovery rounds are activated newest first. If the newest round cannot fit
  completely, the request returns 413 rather than partially activating it. Older
  discovery rounds are removed until the active set fits.
- `max_request_states` and `max_state_bytes` bound retained compact request
  state. Capacity exhaustion returns HTTP 429 with
  `request_state_capacity`; active streams are not FIFO-evicted.
- Unsupported server-executed search and unrepresentable strict schemas return
  HTTP 422 with `unsupported_search_execution` or `invalid_strict_schema`.
- `max_schema_expansion_bytes`, `max_schema_expansion_nodes`, and
  `max_schema_depth` bound strict local-reference expansion. Missing, recursive,
  unsupported, or external references are rejected instead of replaced
  with guessed schemas.

Request completion in all four terminal outcomes releases retained state.

### Custom tools

`strip_custom_tools` defaults to `false`, preserving custom tools on strict
native routes. Setting it to `true` enables a lossy compatibility workaround:
the plugin removes current custom-tool declarations and converts prior
`custom_tool_call` / `custom_tool_call_output` items into equivalent function
history, namespaced calls included. It returns HTTP 422 if `tool_choice` forces
a removed custom tool or if a historical custom call has no string input.
Pre-existing `function_call` / `function_call_output` history is preserved
unchanged, even when a name collides with a removed custom tool. The model
cannot make new custom-tool calls on that route. Enable it only where this
loss of future custom-tool use is acceptable.

## Behaviour guarantees

- Protocol and configuration gates are evaluated per request; stale state for a
  reused request ID cannot widen a rewrite to another client protocol.
- Missing request state causes a fail-open passthrough instead of guessing tool
  identities.
- Nested namespaces remain addressable, including nested paths such as
  `outer__inner__deep`.
- Activated aliases appear consistently in tool declarations and subsequent
  history. Eager and unknown calls retain their original identity.
- `json.Number` decoding preserves large integers during a rewrite.
- Non-matching response and stream payloads are returned without a replacement
  body.
- ABI entry points contain panics and return error envelopes; a plugin panic
  cannot unwind across the cgo boundary.

## Diagnostics

Set `X-Codex-Tool-Search-Shim-Probe: 1` on an isolated request to append
structure-only diagnostics and receive the `X-Codex-Tool-Search-Shim` response
header. `diagnostics_log_path` defaults to
`codex-tool-search-shim-structure.jsonl` in the platform temporary directory.

Diagnostics record protocol structure, tool counts and names, namespaces, call
IDs, budgets, state usage, and rejection reasons. They do not record prompts,
tool arguments, tool results, credentials, or tokens.

## Validation status

Verified by repository tests on 2026-09-26 and runtime checks on 2026-09-27:

- the plugin unit suite and race detector;
- a multi-turn closed loop through the real OpenAI Chat, Claude, and Gemini
  request/response translators, including response-to-Responses restoration;
- a 500-deferred-tool first packet using all three translators;
- a 50-round bridge loop with an 8 KiB active-tool budget, 100 preserved
  historical `call_id` values, a functional final result, and request-state
  release;
- Codex CLI 0.156.1 against an isolated local CLIProxyAPI instance over both
  HTTP/SSE and Responses WebSocket: the model catalog exposed
  `supports_search_tool`, and each run performed two searches, called the
  returned weather and service-status tools, and completed with both results;
- Linux `c-shared` build in `golang:1.26-bookworm` and actual `dlopen`/ABI
  registration (`abi=1`, `register_status=0`, response contained the plugin
  name);
- Windows `c-shared` cross-build with `mingw-w64 14.0.0_3`, producing a
  `PE32+ x86-64` DLL with the expected plugin entry points;
- Windows DLL actual loading under Wine 10.0 (Debian trixie, `x86_64`),
  returning `abi=1 register_status=0 response_bytes=2803 has_name=1` with
  exit code 0;
- real-provider `input_tokens` A/B for the same 100 deferred-tool workload:
  Antigravity OAuth reported `19,660` before and `612` after the plugin;
- host model-list capability metadata tests;
- dynamic-library build and pluginhost load/interceptor/lifecycle validation.

Not yet verified by this review:

- real provider/model search quality or upstream acceptance (the Codex runs
  used a local mock Chat upstream);
- generalization of the measured token reduction across providers and workloads.

Unverified transports and platforms are not claimed as supported.

## Build and install

From the repository root on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/codex-tool-search-shim.dylib \
  ./examples/plugin/codex-tool-search-shim/go
rm -f plugins/darwin/$(go env GOARCH)/codex-tool-search-shim.h
```

Use `.so` on Linux or FreeBSD and `.dll` on Windows. The output filename must
match the plugin ID.

The repository Makefile owns the normal build and installation paths:

```bash
make -C examples/plugin build-codex-tool-search-shim
make -C examples/plugin install-codex-tool-search-shim
```

`install-codex-tool-search-shim` defaults to `$HOME/.cliproxyapi/plugins`;
override it with `CLIPROXYAPI_PLUGIN_DIR`. Restart CLIProxyAPI after installing
the new library, then verify `/healthz`.
