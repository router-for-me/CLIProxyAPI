# otelplugin

OpenTelemetry usage exporter for CLIProxyAPI. Emits one OTLP span per upstream
LLM call, with attributes following the emerging GenAI semantic conventions,
plus W3C Baggage propagation for caller-supplied agent identity.

## Why

CLIProxyAPI already has a first-class `usage.Plugin` pipeline (see
`sdk/cliproxy/usage/manager.go`). The in-memory `LoggerPlugin` and the
`redisqueue` plugin both consume `usage.Record` values via the manager. This
package adds a third sink — OpenTelemetry — so operators can ship per-request
cost + token telemetry into any OTLP backend (Honeycomb, Jaeger, Tempo,
Azure Monitor, Datadog, etc.) without writing a custom shim.

The plugin is **off by default**. Setting `telemetry.otlp.enabled: true` in
config turns it on. No other behaviour changes.

## Wire-format outputs

### Per-span attributes (OpenTelemetry GenAI semconv where it exists)

| Attribute                                  | Type   | Notes                                           |
| ------------------------------------------ | ------ | ----------------------------------------------- |
| `gen_ai.system`                            | string | Provider id (`anthropic`, `openai`, …)          |
| `gen_ai.request.model`                     | string | Model id from `usage.Record.Model`              |
| `gen_ai.request.model_alias`               | string | Alias when one was used                         |
| `gen_ai.request.reasoning_effort`          | string | Client-requested thinking level when supplied   |
| `gen_ai.request.source`                    | string | Upstream auth source (`chatplan`, `apikey`, …)  |
| `gen_ai.response.status_code`              | int    | Set only on failed calls                        |
| `gen_ai.usage.input_tokens`                | int    | Prompt tokens                                   |
| `gen_ai.usage.output_tokens`               | int    | Completion tokens                               |
| `gen_ai.usage.cache_read_input_tokens`     | int    | Anthropic prompt-cache hits (when > 0)          |
| `gen_ai.usage.cache_creation_input_tokens` | int    | Anthropic prompt-cache writes (when > 0)        |
| `gen_ai.usage.reasoning_tokens`            | int    | Provider-reported reasoning tokens (when > 0)   |
| `gen_ai.usage.total_tokens`                | int    | Provider-reported total (when > 0)              |
| `cost.usd`                                 | float  | Total — emitted only when `cost.enabled: true`  |
| `cost.input_usd`                           | float  | Component breakdown                             |
| `cost.output_usd`                          | float  | Component breakdown                             |
| `cost.cache_read_usd`                      | float  | Component breakdown                             |
| `cost.cache_creation_usd`                  | float  | Component breakdown                             |
| `<baggage key>`                            | string | Each W3C Baggage key from the inbound request   |

Span name defaults to `gen_ai.request`; configurable via `span.name`.

### Resource attributes

`service.name` defaults to `cli-proxy-api`. `service.namespace` is optional.

## Config

```yaml
telemetry:
  otlp:
    enabled: true                         # off by default
    endpoint: "http://127.0.0.1:4318"     # OTLP HTTP endpoint
    protocol: "http/protobuf"             # only http/protobuf supported today
    headers:                              # optional, e.g. for hosted backends
      x-honeycomb-team: "${HONEYCOMB_API_KEY}"
    service:
      name: "cli-proxy-api"
      namespace: "agent-platform"
    span:
      name: "gen_ai.request"
      include_baggage_keys: ["agent.id", "workload.kind"]   # [] = all keys
      include_usage: true                 # set false to drop token counts
      include_cost: true                  # set false to drop cost.* attributes

  baggage:
    propagation: "off"                    # off | propagate | allowlist
    allowed_keys: ["agent.id", "workload.kind"]

  cost:
    enabled: false                        # opt-in pricing table
    pricing:
      claude-opus-4-7:
        input_per_million: 15.0
        output_per_million: 75.0
        cache_read_per_million: 1.5
        cache_creation_per_million: 18.75
```

## Caller-supplied identity (W3C Baggage)

The plugin works without any baggage — it still emits gen_ai.* attributes
from `usage.Record`. To attribute cost back to **which agent runtime** issued
the request, callers can set a [W3C Baggage](https://www.w3.org/TR/baggage/)
header on each inbound HTTP request:

```
baggage: agent.id=builder,agent.session.id=01HJX,workload.kind=chat-turn
```

When the Gin middleware (`otelplugin.Middleware()`) is registered, those keys
land on the request context. The plugin reads them via
`BaggageFromContext(ctx)` and copies the allowlisted keys to span attributes.

The baggage propagation policy controls what flows from CLIProxyAPI to the
upstream provider:

- `off` — strip baggage at the boundary. The upstream provider sees no baggage.
- `propagate` — forward verbatim.
- `allowlist` — forward only the keys in `baggage.allowed_keys`.

Operators set the policy based on their trust posture toward upstream
providers. `off` is safe-by-default.

## How it integrates with CLIProxyAPI

```
inbound HTTP
   │
   ▼
[ otelplugin.Middleware ] ── parses baggage → attaches to ctx
   │
   ▼
[ existing proxy handlers ]
   │
   ▼
upstream LLM call
   │
   ▼ (success or failure)
[ runtime/executor publishes usage.Record ]
   │
   ▼
[ usage.Manager ]
   │
   ├─ LoggerPlugin (existing)
   ├─ usageQueuePlugin (existing, redisqueue)
   └─ otelplugin.Plugin (this package) ── emits OTLP span ──▶ otel-collector
```

The pipeline is exactly the existing one. We add a plugin; we do not modify
runtime/executor or the manager itself.

## Wiring (server-side)

The plugin auto-registers via `init()` (same pattern as `redisqueue`).
Server-side wiring is one line in `internal/api/server.go`:

```go
engine.Use(otelplugin.Middleware())
```

And one line in the config loader after parsing the YAML block:

```go
otelplugin.SetConfig(parsedConfig.Telemetry)
```

That is the complete integration footprint. The plugin is otherwise self-
contained — config + lifecycle + tests all live under
`internal/otelplugin/`.

## Test coverage

```
go test ./internal/otelplugin/... -count=1 -v
```

28 unit tests cover:

- W3C Baggage parsing (empty, multi-key, URL-decoding, per-entry metadata, case handling, malformed entries)
- Baggage round-trip through context
- Filter-by-allowlist
- Gin middleware (header present, absent, malformed)
- Outbound propagation (off, propagate, allowlist, nil-request safety)
- GenAI semconv attribute mapping
- Optional-field omission when zero
- `include_usage: false` / `include_cost: false` toggles
- Cost table exact + prefix match, fallback when model is absent
- `startTimeFor` precedence (RequestedAt > Latency-derived > Now)
- Disabled-plugin no-op

## Open questions for the maintainers

We have implementation flexibility on the following — happy to follow
whatever direction the maintainers prefer:

1. **Config block placement.** Currently a top-level `telemetry:` block. Could
   also live under an existing namespace (e.g. `usage-statistics-*` keys).
2. **gRPC exporter.** Only `http/protobuf` is wired today. Adding gRPC is one
   extra import + parallel switch; happy to follow up if there's demand.
3. **Span name default.** Used `gen_ai.request` per the OpenTelemetry GenAI
   semconv working draft. Some operators may prefer `llm.request` or
   `cli-proxy-api.request`. Easily made configurable; default is the question.
4. **Build-tag gating.** The OTel SDK adds ~10MB to the binary. If that
   matters, the package can sit behind a build tag (`-tags otelplugin`) and
   ship a no-op stub for the default build.
