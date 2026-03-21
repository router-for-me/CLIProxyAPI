# CLIProxyAPI Auth/Codex Fork

English | [中文](README_CN.md)

This repository is an independently maintained derivative of `router-for-me/CLIProxyAPI`.

It is not the upstream project, is not affiliated with `router-for-me`, and should not be represented as an official mirror, release channel, support channel, or documentation endpoint for the upstream repository.

## What This Fork Is

This fork keeps the original CLI proxy compatibility goals while carrying a smaller set of local changes focused on runtime behavior instead of product promotion.

Current fork-specific changes relative to `router-for-me/CLIProxyAPI`:

- Codex/OpenAI Responses request translation adjustments and executor wiring updates
- Auth scheduler behavior changes aimed at lower churn under concurrency
- Async auth persistence additions
- Scheduler benchmark and persistence test updates
- Container defaults adjusted for this repository's GHCR image distribution

At the moment, the Go module path is still `github.com/router-for-me/CLIProxyAPI/v6` for compatibility with the existing code layout. That compatibility detail does not imply any project affiliation.

## Core Capabilities

- OpenAI, Gemini, Claude, and Codex compatible API endpoints for CLI-oriented clients
- OAuth-based support for Codex, Claude Code, Qwen Code, and iFlow
- Streaming and non-streaming responses
- Multi-account routing and load balancing
- Reusable Go SDK under `sdk/cliproxy`
- Request translation and provider execution layers suitable for embedding

## Quick Start

Use the fork's GHCR image:

```bash
docker run --rm -p 8317:8317 ghcr.io/arron196/cliproxyapi:latest
```

Or with Compose:

```bash
docker compose up -d
```

The default image in `docker-compose.yml` is:

```text
ghcr.io/arron196/cliproxyapi:latest
```

## Local Documentation

- SDK usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- SDK advanced topics: [docs/sdk-advanced.md](docs/sdk-advanced.md)
- SDK access/auth: [docs/sdk-access.md](docs/sdk-access.md)
- SDK watcher integration: [docs/sdk-watcher.md](docs/sdk-watcher.md)

Management endpoints are exposed under `/v0/management` when enabled in configuration. This fork does not currently publish a separate external documentation site.

## Project Identity

- Upstream base: `router-for-me/CLIProxyAPI`
- This repository: independent derivative maintained in a separate GitHub repository
- Upstream relationship: no affiliation, no endorsement, no shared release process, no shared support obligation

If you need upstream behavior or upstream support, use the upstream repository directly.

## Contributing

Contributions should target this repository's behavior and documentation, not the upstream project's release promises or commercial integrations.

## License

MIT. See [LICENSE](LICENSE).
