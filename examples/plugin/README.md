# Standard Dynamic Library Plugin Examples

This directory contains standard dynamic library plugin examples for the CLIProxyAPI C ABI.

## Layout

- `simple/`: full provider-native skeleton that declares every supported capability.
- `model/`: model capability only.
- `auth/`: auth provider capability only.
- `frontend-auth/`: frontend auth provider capability only.
- `frontend-auth-exclusive/`: frontend auth provider that becomes the only request authentication provider when selected.
- `executor/`: executor capability only.
- `protocol-format/`: minimal executor focused on input/output format declarations.
- `request-translator/`: request translation capability only.
- `request-normalizer/`: request normalization capability only.
- `codex-service-tier/`: Go-only request normalizer that sets Codex `gpt-5.4` requests to the priority service tier when enabled.
- `scheduler/`: Go-only scheduler that can select a configured auth ID, delegate to a built-in scheduler, or deny picks.
- `response-translator/`: response translation capability only.
- `response-normalizer/`: response normalization capability only.
- `thinking/`: thinking applier capability only.
- `usage/`: usage observer capability only.
- `cli/`: command-line capability only.
- `management-api/`: Management API and resource capability only.
- `host-callback/`: minimal plugin resource that demonstrates host callbacks.

Most standard capability examples contain `go/`, `c/`, and `rust/` subdirectories. Specialized examples may provide only the implementation language they need.

## Codex Service Tier

`codex-service-tier` declares the request normalization capability. When `fast` is `true`, it sets `service_tier` to `priority` for requests where `req.ToFormat` is `codex` and `req.Model` is `gpt-5.4`.

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

## Scheduler

`scheduler` declares the scheduler capability. It can select a configured auth ID from the candidate list, delegate to the built-in `fill-first` or `round-robin` scheduler, or reject picks when `deny` is `true`.

```yaml
plugins:
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

`auth_id` selects a matching candidate when `delegate` is empty. `delegate` accepts `""`, `fill-first`, or `round-robin`; other non-empty values leave the pick unhandled. `deny` returns a scheduler error.

## Build All Examples

```bash
make -C examples/plugin list
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin`.

## Notes

`protocol-format` uses a minimal executor because format declarations belong to executor capabilities.

`host-callback` uses a minimal plugin resource because host callbacks are invoked from plugin methods and are not standalone capabilities.

Menu resources returned by `management.register` through the `resources` field are exposed by CPA under `/v0/resource/plugins/<pluginID>/...`. Authenticated plugin Management API routes remain under `/v0/management/...`.
