# Responses Client Tool Protocol (Core)

This document describes the core handling of the Responses client tool
protocol: client-executed `tool_search` discovery and `custom` tools. The
handling lives entirely in the core and requires no plugin or dynamic
library.

## Goals and non-goals

Goals:

- Complete client `tool_search` and `custom` bridging with core features only,
  without installing any dynamic library.
- Convention over configuration: routes that declare nothing still get a
  correct policy, derived from what the runtime already knows about the call.
- Resolve the strategy from the actual upstream route (selected provider,
  auth kind, real upstream model, actual protocol, endpoint scoping), never
  from client aliases.
- Declare `supports_search_tool` only when every selectable route behind a
  public model alias can complete the client search loop.

Non-goals: a Muse-specific provider, a tool execution server, a second
generic JSON-RPC ABI, automatic model fallback, or a cross-session tool
content database.

## Configuration

Top-level `responses-tools` is the only configuration source. There is no
second policy in `models.json`, model aliases, or plugin YAML. Every field is
optional: an absent block already applies the convention defaults.

```yaml
responses-tools:
  enabled: true            # emergency gate; omit or set false to disable all
  limits:
    max-active-tool-bytes: 262144
    max-active-attempts: 512
    max-state-bytes: 33554432
    max-attempt-bytes: 1048576
    max-schema-expansion-bytes: 65536
    max-schema-expansion-nodes: 10000
    max-depth: 64
  routes:                   # optional overrides of the convention defaults
    - match:
        provider: "codex"
        auth-kind: "oauth"
        upstream-model: "gpt-5.6"
        upstream-format: "codex"
      client-search: "bridge"
      custom-tools: "function"
      custom-grammar: "reject"
      schema:
        complete-search-required: false
        local-refs: "preserve"
```

## Defaults

Nothing needs to be configured for the common case. The effective policy is
derived from the upstream format alone:

- `client-search: native` for upstreams that speak the Responses protocol
  natively (`openai-response`, `codex`). Nothing is rewritten and the native
  passthrough path — including live duplex steering — is preserved.
- `client-search: bridge` for every other upstream. The bridge stays inert
  until a client actually declares `tool_search`, so requests without it are
  untouched.
- `custom-tools: inherit` everywhere. The core never infers custom handling.
- `custom-grammar: reject` and `schema.local-refs: preserve`. No lossy
  strategy is ever inferred.

Lossy strategies stay opt-in, because each of them either drops a capability
or rewrites user schema:

- `custom-tools: strip` and `custom-grammar: describe`
- `schema.complete-search-required: true` and `schema.local-refs: inline`
- `client-search: disabled`, to turn search off for one route

`enabled: false` is the emergency gate: it turns the whole feature off without
deleting any other configuration.

Strategy enums:

- `client-search`: `inherit` (follow the convention for this route), `native`
  (passthrough on a true native Responses route), `bridge` (function translation),
  `disabled` (explicit client search requests fail with 422, never silently
  dropped).
- `custom-tools`: `inherit`, `native`, `function` (full wrap and restore),
  `strip` (lossy: drops future declarations but keeps convertible history;
  forced calls to stripped tools fail with 422), `reject` (any custom
  contract fails with 422).
- `custom-grammar`: `reject` (default), `describe` (function mode only:
  folds grammar syntax into the description; sampling constraints are not
  enforced upstream).
- `schema.complete-search-required`: adapts only client-search schemas, never
  strictifies every tool globally.
- `schema.local-refs`: `preserve` (default) or `inline` (expands local
  references; recursive, external, missing, or unsupported references fail
  instead of deleting constraints).

Match rules:

- `provider`, `auth-kind`, `upstream-model`, and `upstream-format` are all
  required exact conditions. No wildcards, regex, or capability inference from
  model names are supported.
- API-key routes must set `base-url` so the same provider/model name on
  different endpoints cannot share one rule. OAuth routes leave it empty.
- Overlapping rules with conflicting strategies are rejected at load time;
  dynamic conflicts at routing time return an explicit configuration error,
  never a silent first-match.
- Limits must be positive with reasonable upper bounds; negative values never
  mean unlimited.

## Rollback

Set `responses-tools.enabled: false`. No other configuration is affected, and
the convention policy stops applying on the next request. When changing route
rules, wait for active requests to drain so no request changes protocol
semantics mid-flight.

## Budgets

| Setting | Meaning |
|---|---|
| `limits.max-active-tool-bytes` | Declared tool bytes retained per attempt |
| `limits.max-active-attempts` | Concurrent retained attempts, Manager scope |
| `limits.max-state-bytes` | Shared Manager state budget |
| `limits.max-attempt-bytes` | Per-attempt state ceiling |
| `limits.max-schema-expansion-*` | Schema rewriting ceilings |

Diagnostics carry only route/policy generation, tool kind counts,
declaration bytes, used budget, and fixed reason codes. Arguments, patches,
grammar bodies, prompts, results, tokens, and credentials are never logged.

## Boundaries

- `interactions`, direct plugin-executor routes, custom unknown executors,
  images/video, and `responses/compact` never enable the bridge; they keep
  their existing paths and make no new search capability claims.
- A configured rule that hits an unsupported target returns an explicit 422
  instead of silently passing through.
- `CountTokens` shares request preparation and declaration budget but builds
  no response stream state; executors that cannot count surface their own
  error behavior.
- Native stateful passthrough/steering: only passthrough that needs no
  transformation is allowed. Bridged ordinary WebSocket traffic uses replay;
  live steering combinations that need bridging are rejected explicitly, and
  live duplex is not bridged frame-by-frame.
- Server-side history: only locally expandable history converts; requests
  that depend on remote opaque `previous_response_id` references fail with
  422 on routes that need transformation. Native paths keep their existing
  capabilities.
