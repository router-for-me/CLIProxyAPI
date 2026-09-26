# Responses Client Tool Protocol (Core)

This document describes the core handling of the Responses client tool
protocol: client-executed `tool_search` discovery and `custom` tools. It is
the formal migration target for the legacy `codex-tool-search-shim` example
plugin: the plugin leaves the production path and stays as reference only.

## Goals and non-goals

Goals:

- Complete client `tool_search` and `custom` bridging with core features only,
  without installing any dynamic library.
- Keep default deployments unchanged; only explicitly matched routes enter
  the new adaptation, everything else keeps its current behavior.
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
second policy in `models.json`, model aliases, or plugin YAML.

```yaml
responses-tools:
  enabled: false
  limits:
    max-active-tool-bytes: 262144
    max-active-attempts: 512
    max-state-bytes: 33554432
    max-attempt-bytes: 1048576
    max-schema-expansion-bytes: 65536
    max-schema-expansion-nodes: 10000
    max-depth: 64
  routes:
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

Strategy enums:

- `client-search`: `inherit` (legacy behavior), `native` (passthrough on a
  true native Responses route), `bridge` (function translation),
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
  required exact conditions. The first release supports no wildcards, regex,
  or capability inference from model names.
- API-key routes must set `base-url` so the same provider/model name on
  different endpoints cannot share one rule. OAuth routes leave it empty.
- Overlapping rules with conflicting strategies are rejected at load time;
  dynamic conflicts at routing time return an explicit configuration error,
  never a silent first-match.
- Limits must be positive with reasonable upper bounds; negative values never
  mean unlimited.

## Migration and rollback

1. Keep a backup of the working configuration.
2. In a single configuration change, disable the legacy shim
   (`plugins.configs.codex-tool-search-shim.enabled: false`) and enable the
   core with exact routes (`responses-tools.enabled: true` plus routes).
   Enabling both at once is rejected with an explicit error.
3. Wait for active requests to drain before switching instances, so no
   request changes protocol semantics mid-flight.
4. Roll back by reversing the same switch with the kept backup.

Legacy plugin mapping:

| Legacy plugin setting | Core equivalent |
|---|---|
| `bridge_models` | Derived from effective client-search capability across real routes, not a model allowlist |
| implicit translation-upstream bridge | explicit route plus `client-search: bridge` |
| `bridge_native_models` | explicit native route plus `client-search: bridge` |
| `strict_responses_models` wildcard | expanded to concrete routes with matching schema policy; wildcards are not migrated mechanically |
| `strip_custom_tools: true` | `custom-tools: strip` (never auto-upgraded to `function`) |
| `strip_custom_tools: false` | `custom-tools: inherit` (or `native`) |
| `max_active_tool_bytes` | `limits.max-active-tool-bytes` |
| `max_request_states` | `limits.max-active-attempts` (Manager scope) |
| `max_state_bytes` | shared Manager budget, no longer a plugin map |
| schema budgets | same kebab-case limits |
| `extra_source_formats` | known-protocol registry resolution; unknown aliases are never treated as Responses |
| `diagnostics_log_path` / probes | structured diagnostic outlets, redacted; no arbitrary file writes |

Diagnostics carry only route/policy generation, tool kind counts,
declaration bytes, used budget, and fixed reason codes. Arguments, patches,
grammar bodies, prompts, results, tokens, and credentials are never logged.

## Boundaries

- `interactions`, direct plugin-executor routes, custom unknown executors,
  images/video, and `responses/compact` never enable the new bridge; they
  keep their legacy paths without new search capability claims.
- A configured rule that hits an unsupported target returns an explicit 422
  instead of silently passing through.
- `CountTokens` shares request preparation and declaration budget but builds
  no response stream state; executors that cannot count keep their original
  error behavior.
- Native stateful passthrough/steering: only passthrough that needs no
  transformation is allowed. Bridged ordinary WebSocket traffic uses replay;
  live steering combinations that need bridging are rejected explicitly, and
  the first release does not bridge live duplex frame-by-frame.
- Server-side history: only locally expandable history converts; requests
  that depend on remote opaque `previous_response_id` references fail with
  422 on routes that need transformation. Native paths keep their existing
  capabilities.
