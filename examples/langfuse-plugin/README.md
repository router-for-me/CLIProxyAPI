# Langfuse observability plugin

This example shows how to build a CLIProxyAPI binary that forwards every
upstream request to [Langfuse](https://langfuse.com) as a generation span.

## How it works

`sdk/cliproxy/usage` exposes a `Plugin` interface. The proxy runtime calls
`HandleUsage` after each upstream request completes. This plugin packages the
record into a Langfuse `generation-create` event and ships it in a background
goroutine so the response path is not blocked.

When an upstream gateway populates `X-Request-Id`, the span is attached to
the matching parent trace. Without it a standalone trace is created per
request.

For richer input/output capture the proxy runtime also writes per-request
context keys (see `sdk/cliproxy/usage` constants). These are populated when
the runtime's observability path is active and contain the first user message,
accumulated response text, and a token usage breakdown.

## Build

```sh
go build -o cpa-langfuse ./examples/langfuse-plugin
```

## Run

```sh
LANGFUSE_BASE_URL=https://cloud.langfuse.com \
LANGFUSE_PUBLIC_KEY=pk-lf-... \
LANGFUSE_SECRET_KEY=sk-lf-... \
./cpa-langfuse -config config.yaml
```

If the environment variables are not set the binary starts normally without
sending any data to Langfuse.

## What gets sent

Each upstream request produces one `generation` event:

| Field | Value |
|---|---|
| `traceId` | `X-Request-Id` from the inbound request, or a fresh UUID |
| `name` | `cpa.upstream` |
| `model` | upstream model name |
| `input` | first user message text (when available) |
| `output` | response text (when available) |
| `usage` | input/output/cache/reasoning token counts |
| `metadata` | provider, auth_id, latency_ms, upstream_url |
| `level` | `ERROR` on upstream failure, `DEFAULT` otherwise |
