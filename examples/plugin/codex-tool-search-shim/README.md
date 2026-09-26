# Codex Tool Search Shim

This Go dynamic-library plugin adapts the Codex client-executed
`tool_search` protocol for CLIProxyAPI routes whose upstream does not speak
OpenAI Responses, and it repairs Codex tool declarations for native Responses
upstreams that validate schemas more strictly than OpenAI does.

It has two modes:

- **Bridge mode** (default) for Responses clients (`SourceFormat` of
  `openai-response`) whose selected `ToFormat` is a translated upstream format
  such as `openai`. Native Responses/Codex targets are left untouched, and so is
  every other client protocol.
- **Strict native mode** for opted-in native Responses models that reject
  Codex declarations. Nothing changes until a model is listed in
  `strict_responses_models`.

Both modes are protocol-gated. Requests, responses and stream chunks that did
not come from a Responses client are never rewritten, so chat-completions,
Claude and Gemini traffic that shares the proxy keeps its own tool declarations
and payload bytes.

It keeps the first request bounded by:

- the ordinary non-deferred tools;
- one small ordinary `tool_search` function declaration;
- a name-only index for namespace children.

Namespace child schemas are not sent until the conversation returns a
`tool_search_output` containing those tools. The plugin then promotes only the
discovered children as flat function declarations. Search calls and outputs are
converted into ordinary function-call history, and provider responses are
converted back into native Responses `tool_search_call` and namespaced
`function_call` items for Codex.

The ordinary `tool_search` declaration is only added when the request actually
has something to search: a client-executed `tool_search` declaration, tool
search history, or namespaced children that are about to move into the index.
A request that declares neither is forwarded untouched.

## Behaviour guarantees

- Client protocol wins over cached request state. A response is only translated
  for a Responses client, even if a request ID was reused.
- Nested namespaces keep their qualified name, so `outer` > `inner` > `deep`
  stays addressable as `outer__inner__deep`.
- Tool search history items that omit `execution` are treated as
  client-executed, which is what Responses-compatible clients emit.
- Requests that contain neither `tool_search` nor `namespace` never reach the
  JSON parser, and stream chunks that cannot contain a `function_call` are
  returned untouched without a parse.
- Unchanged responses and stream chunks return no body, so the host does not
  clone and replace the payload on every chunk.
- A panic inside the plugin is converted into an error envelope. The plugin
  never unwinds a panic across the cgo boundary, which would abort the host
  process.
- Request payloads are decoded with `json.Number`, so large integers survive a
  rewrite unchanged.

## Configuration

```yaml
plugins:
  configs:
    codex-tool-search-shim:
      enabled: true
      priority: 100
      strict_responses_models:
        - "*muse-spark*"
      # Optional. Extra client protocol identifiers to treat as
      # Responses-family. Built-ins: openai-response, openai-responses,
      # responses, response, codex.
      extra_source_formats: []
      # Optional. Probe diagnostics target. Defaults to
      # codex-tool-search-shim-structure.jsonl in the platform temp directory.
      diagnostics_log_path: ""
```

The plugin bypasses native Responses/Codex targets, except that models matching
`strict_responses_models` get two extra request repairs:

- every property of a client-executed `tool_search` declaration becomes
  required, previously optional properties widen to accept `null`, and
  `additionalProperties` is pinned to `false`; `type` and `execution` stay
  untouched so Codex still receives a locally resolved `tool_search_call`.
- local `$defs` / `definitions` references are inlined and re-entrant
  references collapse into an opaque object, because some upstreams reject
  recursive JSON schemas.

Entries match the selected upstream model and the client-requested model.
Matching is case-insensitive and `*` is a wildcard, so `muse-spark-*` matches a
prefix and `*muse-spark*` matches a substring. Quote entries that start with
`*` when writing YAML. Strict repairs still only run for Responses clients.

## Build

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

## Install

The repository Makefile owns the build and installation paths so a local
CLIProxyAPI update can refresh the plugin in the same operation:

```bash
make -C examples/plugin build-codex-tool-search-shim
make -C examples/plugin install-codex-tool-search-shim
```

`install-codex-tool-search-shim` defaults to
`$HOME/.cliproxyapi/plugins`; override it with `CLIPROXYAPI_PLUGIN_DIR`.
Restart CLIProxyAPI after installing so the new library is mapped, then verify
`/healthz` before sending traffic.

## Diagnostics

Set `X-Codex-Tool-Search-Shim-Probe: 1` on an isolated request to enable the
structure-only diagnostic log and receive the
`X-Codex-Tool-Search-Shim` response header. Normal traffic does not write
diagnostic files. The log is appended to
`codex-tool-search-shim-structure.jsonl` in the platform temporary directory;
set `diagnostics_log_path` to point it somewhere else.

Diagnostics contain protocol types, tool names, namespaces, call IDs, and
deferred-loading flags only. They do not contain credentials, prompts, tool
arguments, or tool outputs.
