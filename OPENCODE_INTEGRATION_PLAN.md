# OpenCode Provider Integration Plan

This document is the contract for adding first-class [OpenCode](https://opencode.ai)
support to CLIProxyAPI. It is written against the existing **Amp CLI** module
(`internal/api/modules/amp/`) as the reference pattern.

## 1. What "OpenCode provider support" means here

OpenCode (`sst/opencode`) is a terminal coding agent built on the Vercel AI SDK.
It talks to upstreams through standard, well-known protocol surfaces selected by
the npm package configured for a provider block in `opencode.json`:

| OpenCode `npm` package      | Wire protocol            | Path it POSTs to (relative to `baseURL`) |
| --------------------------- | ------------------------ | ---------------------------------------- |
| `@ai-sdk/openai-compatible` | OpenAI Chat Completions  | `/chat/completions`, `/completions`      |
| `@ai-sdk/openai`            | OpenAI Responses         | `/responses`                             |
| `@ai-sdk/anthropic`         | Anthropic Messages       | `/messages` (baseURL already ends `/v1`) |

Sources (fetched 2026-06-01):
- https://opencode.ai/docs/providers/ — custom provider block, `npm`, `options.baseURL`,
  `options.apiKey` (`{env:VAR}`), `options.headers`, `models` map. Confirms
  `@ai-sdk/openai-compatible` hits `/v1/chat/completions` and `@ai-sdk/openai` hits
  `/v1/responses`.
- https://opencode.ai/docs/config/ — `opencode.json` schema, `provider` block,
  `provider/model` model naming.

**Conclusion:** OpenCode is an OpenAI-compatible / Anthropic-compatible client. It
needs nothing the proxy cannot already serve. "First-class support" therefore means
giving OpenCode a **dedicated, isolated route namespace** (so a single `baseURL`
serves all three protocols and OpenCode-specific model mappings apply only to
OpenCode traffic), exactly mirroring how the Amp module exposes merged +
provider-scoped routes — minus the parts that are specific to Amp's proprietary
control plane.

## 2. Protocol surfaces

All three, reusing existing SDK handlers (no new translator code — OpenCode's wire
formats are byte-for-byte the formats the proxy already speaks):

- OpenAI Chat Completions — `openai.NewOpenAIAPIHandler(...).ChatCompletions` / `.Completions`
- OpenAI Responses — `openai.NewOpenAIResponsesAPIHandler(...).Responses`
- Anthropic Messages — `claude.NewClaudeCodeAPIHandler(...).ClaudeMessages` / `.ClaudeCountTokens`
- (bonus, for parity with Amp) Gemini native — `gemini.NewGeminiAPIHandler(...)`

### Routes

**Merged** (provider resolved from the request model — one `baseURL` for everything):

```
GET  /opencode/v1/models
POST /opencode/v1/chat/completions
POST /opencode/v1/completions
POST /opencode/v1/responses
POST /opencode/v1/messages
POST /opencode/v1/messages/count_tokens
GET  /opencode/v1beta/models
GET  /opencode/v1beta/models/*action
POST /opencode/v1beta/models/*action
```

**Provider-scoped** (mirrors Amp's `/api/provider/:provider/...` for explicit
protocol selection / backend pinning):

```
GET  /opencode/provider/:provider/models
GET  /opencode/provider/:provider/v1/models
POST /opencode/provider/:provider/v1/chat/completions
POST /opencode/provider/:provider/v1/completions
POST /opencode/provider/:provider/v1/responses
POST /opencode/provider/:provider/v1/messages
POST /opencode/provider/:provider/v1/messages/count_tokens
GET  /opencode/provider/:provider/v1beta/models
GET  /opencode/provider/:provider/v1beta/models/*action
POST /opencode/provider/:provider/v1beta/models/*action
```

A top-level `/opencode` prefix (rather than reusing Amp's `/api/provider`) keeps the
integration fully isolated from Amp's `/api` management middleware and lets OpenCode
model mappings be scoped to OpenCode requests only.

## 3. What we intentionally do NOT copy from Amp (rejected paths)

Amp's module also contains a reverse proxy to `ampcode.com`, localhost-restricted
management routes, multi-source secret resolution, and per-client upstream API-key
mapping. These exist solely to bridge Amp's proprietary control plane (OAuth, threads,
credits). **OpenCode has no such mandatory control plane** — you point a custom
provider's `baseURL` at the proxy and that is the entire integration. Replicating that
machinery would be cargo-culting, would enlarge the diff, and contradicts the
"only add code where OpenCode genuinely differs" guardrail. Rejected.

Consequently the OpenCode module is a **trimmed mirror**: route aliases + a
config-driven model-mapping layer, backed directly by the existing SDK handlers.

## 4. Model-mapping / alias strategy

Mirror Amp's `model-mappings` (`from` → `to`, optional `regex`) so OpenCode can request
model names it knows about and have them routed to a locally-available OAuth/provider
model. Behavior:

1. Extract the requested model from the body (or Gemini URL path).
2. If a provider is available locally for that model, use it as-is.
3. Otherwise (or always, when `force-model-mappings: true`) consult the mapping table;
   if a mapping resolves to a model with an available provider, apply it where the model
   actually lives — rewrite the request body's `model` field for OpenAI/Claude, or
   rewrite the Gemini native `:action` path param (`model:method`) since the shared
   `GeminiHandler` resolves the model from the URL, not the body — and proceed.
4. If nothing resolves, the underlying handler returns its normal error (no silent
   forwarding to an external paid gateway — OpenCode users point at us deliberately).

Dynamic thinking suffixes (e.g. `(8192)`, `(xhigh)`) are preserved through mapping,
identical to the Amp mapper semantics.

## 5. Files added / modified

### Added
- `internal/api/modules/opencode/opencode.go` — `OpenCodeModule` implementing
  `modules.RouteModuleV2` (`New`/`Register`/`OnConfigUpdated`/`Name`). Ref: `amp/amp.go`.
- `internal/api/modules/opencode/routes.go` — `registerRoutes` (merged + provider-scoped).
  Ref: `amp/routes.go` `registerProviderAliases`.
- `internal/api/modules/opencode/model_mapping.go` — `DefaultModelMapper`. Ref:
  `amp/model_mapping.go` (self-contained, no `regex`/suffix behavior changes).
- `internal/api/modules/opencode/mapping_handler.go` — handler wrapper applying mappings.
  Ref: trimmed `amp/fallback_handlers.go` (no reverse-proxy fallback).
- `internal/api/modules/opencode/*_test.go` — route resolution, mapping, round-trip tests.
- `examples/opencode-provider/main.go` — minimal runnable client example. Ref: `examples/http-request`.

> Note: the repo gitignores `docs/*` (only the pre-existing `sdk-*.md` files are tracked,
> and the Amp guide itself lives on the external help site). To respect that convention,
> the OpenCode setup guide is folded into the README's **OpenCode Support** section rather
> than added as a new (ignored) `docs/` page.

### Modified
- `internal/config/config.go` — add `OpenCode OpenCode` field + `OpenCode` /
  `OpenCodeModelMapping` structs. Ref: `AmpCode` / `AmpModelMapping`.
- `internal/api/server.go` — construct & register the module; notify it on hot-reload.
  Ref: the three `ampModule` touch-points.
- `sdk/config/config.go` — re-export `OpenCode` type alias for SDK parity. Ref: `AmpCode` alias.
- `config.example.yaml` — documented `opencode:` block. Ref: the `ampcode:` block.
- `README.md` — list OpenCode under integrations next to Amp CLI.

**Not touched:** `internal/translator/` (reused as-is), any existing provider/executor,
Amp module. No existing route or config key changes.

## 6. Config additions (`config.example.yaml`)

```yaml
# OpenCode Integration (https://opencode.ai)
# opencode:
#   # Force model mappings to run before checking local providers (default: false)
#   force-model-mappings: false
#   # Route models OpenCode requests to models available in your local proxy.
#   model-mappings:
#     - from: "claude-sonnet-4-5"
#       to: "gpt-5"
#     - from: "gpt-5-codex"
#       to: "claude-sonnet-4-5-20250929"
```

Point OpenCode at the proxy by setting a custom provider `baseURL` to
`http://127.0.0.1:8317/opencode/v1` and `apiKey` to one of the proxy's `api-keys`.
