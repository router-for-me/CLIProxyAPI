# cliproxyapi++ Docs

`cliproxyapi++` is a high-performance OpenAI-compatible proxy that lets you route one client API surface to many upstream providers.

## Who This Documentation Is For

- Operators running a shared internal LLM gateway.
- Platform engineers integrating existing OpenAI-compatible clients.
- Developers embedding cliproxyapi++ in Go services.
- Incident responders who need health, logs, and management endpoints.

## What You Can Do

- Use one endpoint (`/v1/*`) across heterogeneous providers.
- Configure routing and model-prefix behavior in `config.yaml`.
- Manage credentials and runtime controls through management APIs.
- Monitor health and per-provider metrics for production operations.

## Start Here

1. [Getting Started](/getting-started) for first run and first request.
2. [Install](/install) for Docker, binary, and source options.
3. [Provider Usage](/provider-usage) for provider strategy and setup patterns.
4. [Provider Catalog](/provider-catalog) for provider block reference.
5. [Provider Operations](/provider-operations) for on-call runbook and incident workflows.
6. [Routing and Models Reference](/routing-reference) for model resolution behavior.
7. [Troubleshooting](/troubleshooting) for common failures and concrete fixes.
8. [Planning Boards](/planning/) for source-linked execution tracking and import-ready board artifacts.

## API Surfaces

- [API Index](/api/) for endpoint map and when to use each surface.
- [OpenAI-Compatible API](/api/openai-compatible) for `/v1/*` request patterns.
- [Management API](/api/management) for runtime inspection and control.
- [Operations API](/api/operations) for health and operational workflows.

## Audience-Specific Guides

- [Docsets](/docsets/) for user, developer, and agent-focused guidance.
- [Feature Guides](/features/) for deeper behavior and implementation notes.
- [Planning Boards](/planning/) for source-to-solution mapping across issues, PRs, discussions, and external requests.

## Fast Verification Commands

```bash
# Basic process health
curl -sS http://localhost:8317/health

# List models exposed by your current auth + config
curl -sS http://localhost:8317/v1/models | jq '.data[:5]'

# Check provider-side rolling stats
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Project Links

- [Main Repository README](https://github.com/KooshaPari/cliproxyapi-plusplus/blob/main/README.md)
- [Feature Changes in ++](./FEATURE_CHANGES_PLUSPLUS.md)
