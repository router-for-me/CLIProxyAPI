# Merged Fragmented Markdown

## Source: docsets/developer/internal/architecture.md

# Internal Architecture

A maintainers-first summary of core boundaries and runtime data flow.

## Core Boundaries

1. `cmd/`: process bootstrap and CLI entry.
2. `pkg/llmproxy/api`: HTTP routing and middleware surfaces.
3. `pkg/llmproxy/runtime` and executors: provider translation + request execution.
4. `pkg/llmproxy/auth`: credential loading, OAuth flows, refresh behavior.
5. Management/ops handlers: runtime control, introspection, and diagnostics.

## Request Lifecycle (High Level)

1. Request enters `/v1/*` route.
2. Access middleware validates API key.
3. Model/endpoint compatibility is resolved.
4. Executor constructs provider-specific request.
5. Response is normalized and returned.
6. Metrics/logging capture operational signals.

## Stability Contracts

- `/v1/chat/completions` and `/v1/models` are external compatibility anchors.
- Management APIs should remain explicit about auth and remote-access rules.
- Routing changes must preserve predictable prefix/alias behavior.

## Typical Change Risk Areas

- Model mapping and alias conflicts.
- OAuth token refresh edge cases.
- Streaming response compatibility.
- Backward compatibility for management endpoints.

## Internal Validation Suggestions

```bash
# quick smoke requests
curl -sS http://localhost:8317/health
curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <key>"

# docs validation from docs/
npm run docs:build
```


---
